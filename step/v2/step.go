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

// SetLabel displays label next to the currently running step.
//
// The current label is transient and should be used only to inform the user.
func SetLabel(ctx context.Context, label string) {
	fmt.Println("SetLabel", label)
}

// SetLabelf displays the formatted label next to the currently running step.
//
// The current label is transient and should be used only to inform the user.
func SetLabelf(ctx context.Context, format string, a ...any) {
	SetLabel(ctx, fmt.Sprintf(format, a...))
}

func run(ctx context.Context, name string, f any, inputs, outputs []any) {
	fmt.Println("run", name)
	ins := make([]reflect.Value, len(inputs)+1)
	ins[0] = reflect.ValueOf(ctx)
	for i, v := range inputs {
		if v == nil {
			ins[i+1] = reflect.Zero(reflect.TypeOf(f).In(i + 1))
		} else {
			ins[i+1] = reflect.ValueOf(v)
		}
	}
	outs := reflect.ValueOf(f).Call(ins)
	contract.Assertf(len(outs) == len(outputs),
		"internal error: This function should be typed to return the correct number of results")
	for i, v := range outs {
		outputs[i] = v.Interface()
	}
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
