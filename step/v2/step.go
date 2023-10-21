// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"context"
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
			// This call is mocked, so just set the output
			copy(outputs, retImmediatly.Out)
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
