// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/briandowns/spinner"
	"github.com/pulumi/pulumi/pkg/v3/resource/stack"
)

// Since pipelines write to stdout, there can only be one executing at once.
var current *pipeline

type pipeline struct {
	ctx       context.Context
	title     string
	callstack []string
	failed    error
	spinner   *spinner.Spinner
}

func Call00(name string, f func(context.Context) error) {
	Call00E(name, func(ctx context.Context) error {
		f(ctx)
		return nil
	})
}

func Call00E(name string, f func(context.Context) error) {
	inputs := []any{}
	outputs := make([]any, 0)
	run(name, f, inputs, outputs)
}

func Call10[T any](name string, f func(context.Context, T), i1 T) {
	Call10E(name, func(ctx context.Context, i1 T) error {
		f(ctx, i1)
		return nil
	}, i1)
}

func Call10E[T any](name string, f func(context.Context, T) error, i1 T) {
	inputs := []any{i1}
	outputs := make([]any, 0)
	run(name, f, inputs, outputs)
	return
}

func Call01[R any](name string, f func(context.Context) R) R {
	return Call01E(name, func(ctx context.Context) (R, error) {
		return f(ctx), nil
	})
}

func Call01E[R any](name string, f func(context.Context) (R, error)) R {
	inputs := []any{}
	outputs := make([]any, 1)
	run(name, f, inputs, outputs)
	return outputs[0].(R)
}

func Call11[T, R any](name string, f func(context.Context, T) R, i1 T) R {
	return Call11E(name, func(ctx context.Context, i1 T) (R, error) {
		return f(ctx, i1), nil
	}, i1)
}

func Call11E[T, R any](name string, f func(context.Context, T) (R, error), i1 T) R {
	inputs := []any{i1}
	outputs := make([]any, 1)
	run(name, f, inputs, outputs)
	return outputs[0].(R)
}

func Pipeline(ctx context.Context, name string, steps func(context.Context)) error {
	if current != nil {
		panic("Cannot call pipeline when already in a pipeline")
	}
	current = &pipeline{
		ctx: ctx, title: name,
		spinner: spinner.New([]string{"|", "/", "-", "\\"},
			time.Millisecond*250,
			spinner.WithHiddenCursor(true),
		),
	}
	current.setLabels()
	done := make(chan struct{})
	go func() {
		steps(current.ctx)
		done <- struct{}{}
	}()
	<-done
	return current.failed
}

func (p *pipeline) setLabels() {
	prefix := "# " + p.title + "\n"
	prefix += p.callTree()
	opts := []string{"|", "/", "-", "\\"}
	frame := p.currentFrame()
	name := "main"
	if frame >= 0 {
		name = p.callstack[frame]
	}
	for i, o := range opts {
		opts[i] = fmt.Sprintf("%s [%s]", o, p.currentFrame())
	}
	p.spinner.UpdateCharSet(opts)
	p.spinner.Lock()
	p.spinner.Prefix = prefix
	p.spinner.Unlock()
}

func (p *pipeline) currentFrame() int {
	stack := []int{}
	for i, v := range p.callstack {
		if v != "" {
			stack = append(stack, i)
		} else {
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) == 0 {
		return -1
	}
	return stack[len(stack)-1]
}

func (p *pipeline) callTree() string {
	current := p.currentFrame()
	indent := 4
	var tree bytes.Buffer
	for i, v := range p.callstack {
		if v == "" {
			indent -= 2
			continue
		}

	}
	return tree.String()
}

// Run a function against arguments and set outputs.
func run(name string, f any, inputs, outputs []any) {

}
