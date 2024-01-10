// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

//go:generate go run ./generate/calls.go

type pipeline struct {
	title  string
	failed error

	// The display that the pipeline should use.
	//
	// display may be nil. Call p.getDisplay() for a non-nil display.
	display Display
}

type pipelineKey struct{}

func withPipeline(ctx context.Context, p *pipeline) context.Context {
	return context.WithValue(ctx, pipelineKey{}, p)
}

func getPipeline(ctx context.Context) *pipeline {
	v := ctx.Value(pipelineKey{})
	if v == nil {
		return nil
	}
	return v.(*pipeline)
}

func (p *pipeline) getDisplay() Display {
	if p == nil || p.display == nil {
		return nullDisplay
	}
	return p.display
}

// A pipeline option.
type Option func(opts *options)

type options struct {
	display Display
}

// Launch a pipeline called `name`.
//
// The pipeline will call `steps` with the context necessary to call annotated functions.
func Pipeline(name string, steps func(context.Context), opts ...Option) error {
	return PipelineCtx(context.Background(), name, steps, opts...)
}

// Launch a pipeline called `name`.
//
// The pipeline will call `steps` with the context necessary to call annotated functions.
//
// That context will inherit from `ctx`.
func PipelineCtx(ctx context.Context, name string, steps func(context.Context), opts ...Option) error {
	if getPipeline(ctx) != nil {
		panic("Cannot call `PipelineCtx` when already in a pipeline")
	}

	var options options
	for _, o := range append([]Option{DefaultDisplay}, opts...) {
		o(&options)
	}

	p := &pipeline{
		title:   name,
		display: options.display,
	}

	// If we are initially silent, don't bother to create and start a spinner
	for _, env := range getEnvs(ctx) {
		_, silent := env.(*Silent)
		if silent {
			p.display = nullDisplay
			break
		}
	}

	if err := p.getDisplay().Start(ctx, name); err != nil {
		return fmt.Errorf("failed to start display: %w", err)
	}
	if err := p.getDisplay().Refresh(ctx, getEnvs(ctx)); err != nil {
		return fmt.Errorf("failed initial redisplay: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer func() { close(done) }()

		steps(withPipeline(ctx, p))
	}()
	<-done

	return errors.Join(
		p.getDisplay().Refresh(ctx, getEnvs(ctx)),
		p.getDisplay().Finish(ctx, p.failed == nil),
		p.failed)
}

func mustGetPipeline(ctx context.Context, name string) *pipeline {
	p := getPipeline(ctx)
	if p == nil {
		panic(`Must call "` + name + `" on a context Derived from a Pipeline.`)
	}
	return p
}

// SetLabel displays label next to the currently running step.
//
// The current label is transient and should be used only to inform the user.
func SetLabel(ctx context.Context, label string) {
	p := getPipeline(ctx)
	if p == nil {
		return
	}
	err := p.getDisplay().SetLabel(ctx, label)
	p.handleError([]any{err})

	err = p.getDisplay().Refresh(ctx, getEnvs(ctx))
	p.handleError([]any{err})
}

// SetLabelf displays the formatted label next to the currently running step.
//
// The current label is transient and should be used only to inform the user.
func SetLabelf(ctx context.Context, format string, a ...any) {
	SetLabel(ctx, fmt.Sprintf(format, a...))
}

// Run a function against arguments and set outputs.
func run(ctx context.Context, name string, f any, inputs, outputs []any) {
	p := mustGetPipeline(ctx, name)
	done := make(chan struct{})

	handleErr := func(err error) {
		if err == nil {
			return
		}
		if p.failed == nil {
			p.errExit(err)
		}
	}

	go func() {
		defer func() { close(done) }()
		envs := getEnvs(ctx)
		var retImmediatly ReturnImmediatly
		silent := false
		for _, env := range envs {
			env := env
			if _, ok := env.(*Silent); ok {
				silent = true
			}
			err := env.Enter(ctx, StepInfo{
				name:     name,
				inputs:   inputs,
				pipeline: p.title,
			})
			if errors.As(err, &retImmediatly) {
			} else if err != nil {
				p.errExit(err)
			}
			defer func() { handleErr(env.Exit(ctx, outputs)) }()
		}

		// If we have a silent function, disable the spinner
		if silent {
			handleErr(p.getDisplay().Pause(ctx))
			defer func() { handleErr(p.getDisplay().Resume(ctx)) }()
		}

		handleErr(p.getDisplay().EnterStep(ctx, name))
		handleErr(p.getDisplay().Refresh(ctx, getEnvs(ctx)))
		ins := make([]reflect.Value, len(inputs)+1)
		ins[0] = reflect.ValueOf(ctx)
		for i, v := range inputs {
			if v == nil {
				ins[i+1] = reflect.Zero(reflect.TypeOf(f).In(i + 1))
			} else {
				ins[i+1] = reflect.ValueOf(v)
			}
		}
		if retImmediatly.Out == nil {
			outs := reflect.ValueOf(f).Call(ins)
			contract.Assertf(len(outs) == len(outputs),
				"internal error: This function should be typed to return the correct number of results")
			for i, v := range outs {
				outputs[i] = v.Interface()
			}
		} else {
			fType := reflect.TypeOf(f)
			// Hydrate the saved outputs back from JSON.
			o, err := hydrateTo(retImmediatly.Out, fType.Out)
			p.handleError([]any{err})
			// This call is mocked, so just set the output
			copy(outputs, o)
		}

		p.handleError(outputs)
	}()
	<-done
	handleErr(p.getDisplay().ExitStep(ctx, p.failed == nil))
	handleErr(p.getDisplay().Refresh(ctx, getEnvs(ctx)))

	if p.failed != nil {
		runtime.Goexit()
	}
}

// Hydrate dst with the values from src, type-correcting as necessary.
//
// The primary type correction performed is converting map[string]any to `struct SomeValue {...}`.
//
// It is legal for src to equal dst.
func hydrateTo(src []any, typ func(int) reflect.Type) ([]any, error) {
	dst := make([]any, len(src))
	var err error
	for i := 0; i < len(src); i++ {
		dst[i], err = hydrateValueTo(src[i], typ(i))
		if err != nil {
			return dst, err
		}
	}
	return dst, nil
}

// hydrateValueTo takes a value src and a type typ and attempts to return a version of src
// that is assignable to typ.
func hydrateValueTo(src any, typ reflect.Type) (any, error) {
	// If the src is nil, construct a valid empty value and set that.
	if src == nil {
		v := reflect.New(typ)
		return v.Elem().Interface(), nil
	}

	// If a trivial assignment is ok, then do that.
	if reflect.TypeOf(src).AssignableTo(typ) {
		return src, nil
	}

	// A trivial assignment is not ok, so we need to round-trip through a type
	// aware format. For example, this allows converting a map[string]any to a
	// matching struct.
	v := reflect.New(typ)
	v.Elem().Set(reflect.Zero(typ))

	b, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(b, v.Interface())
	if err != nil {
		return nil, err
	}

	return v.Elem().Interface(), nil
}

func (p *pipeline) handleError(outputs []any) {
	err := outputs[len(outputs)-1]
	if err == nil {
		return
	}
	p.errExit(err.(error))
}

func (p *pipeline) errExit(err error) {
	p.failed = err
	runtime.Goexit()
}

// cast performs a type cast from src to T.
//
// Unlike src.(T), this cast is valid when casting from an untyped nil.
func cast[T any](src any) (t T) {
	if src != nil {
		t = src.(T)
	}
	return t
}
