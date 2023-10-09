// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

type pipeline struct {
	title     string
	callstack []string
	failed    struct {
		err  error
		done chan struct{}
	}
	spinner *spinner.Spinner
}

func Call00(ctx context.Context, name string, f func(context.Context)) {
	Call00E(ctx, name, func(ctx context.Context) error {
		f(ctx)
		return nil
	})
}

func Call00E(ctx context.Context, name string, f func(context.Context) error) {
	inputs := []any{}
	outputs := make([]any, 1)
	run(ctx, name, f, inputs, outputs)
	getPipeline(ctx).handleError(outputs)
}

func Call10[T any](ctx context.Context, name string, f func(context.Context, T), i1 T) {
	Call10E(ctx, name, func(ctx context.Context, i1 T) error {
		f(ctx, i1)
		return nil
	}, i1)
}

func Call10E[T any](ctx context.Context, name string, f func(context.Context, T) error, i1 T) {
	inputs := []any{i1}
	outputs := make([]any, 1)
	run(ctx, name, f, inputs, outputs)
	getPipeline(ctx).handleError(outputs)
	return
}

func Call01[R any](ctx context.Context, name string, f func(context.Context) R) R {
	return Call01E(ctx, name, func(ctx context.Context) (R, error) {
		return f(ctx), nil
	})
}

func Call01E[R any](ctx context.Context, name string, f func(context.Context) (R, error)) R {
	inputs := []any{}
	outputs := make([]any, 2)
	run(ctx, name, f, inputs, outputs)
	getPipeline(ctx).handleError(outputs)
	return outputs[0].(R)
}

func Call11[T, R any](ctx context.Context, name string, f func(context.Context, T) R, i1 T) R {
	return Call11E(ctx, name, func(ctx context.Context, i1 T) (R, error) {
		return f(ctx, i1), nil
	}, i1)
}

func Call11E[T, R any](ctx context.Context, name string, f func(context.Context, T) (R, error), i1 T) R {
	inputs := []any{i1}
	outputs := make([]any, 2)
	run(ctx, name, f, inputs, outputs)
	getPipeline(ctx).handleError(outputs)
	return outputs[0].(R)
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

func Pipeline(name string, steps func(context.Context)) error {
	return PipelineCtx(context.Background(), name, steps)
}

func PipelineCtx(ctx context.Context, name string, steps func(context.Context)) error {
	if getPipeline(ctx) != nil {
		panic("Cannot call pipeline when already in a pipeline")
	}
	current := &pipeline{
		title: name,
		spinner: spinner.New([]string{"|", "/", "-", "\\"},
			time.Millisecond*250,
			spinner.WithHiddenCursor(true),
		),
		failed: struct {
			err  error
			done chan struct{}
		}{done: make(chan struct{})},
	}
	current.setLabels()
	done := make(chan struct{})
	go func() {
		current.spinner.Start()
		steps(withPipeline(ctx, current))
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-current.failed.done:
	}
	current.spinner.Stop()
	return current.failed.err
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
		opts[i] = fmt.Sprintf("%s [%s]", o, name)
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
	indent := 2
	var tree bytes.Buffer
	for i, v := range p.callstack {
		if v == "" {
			indent -= 2
			continue
		} else {
			indent += 2
		}
		prefix := strings.Repeat(" ", indent)
		if current == i {
			prefix = prefix[:indent-3] + "-> "
		}
		tree.WriteString(prefix)
		tree.WriteString(v)
		tree.WriteRune('\n')
	}
	return tree.String()
}

// Run a function against arguments and set outputs.
func run(ctx context.Context, name string, f any, inputs, outputs []any) {
	p := getPipeline(ctx)
	done := make(chan struct{})
	go func() {
		ctx, envs := popEnvs(ctx)
		for _, env := range envs {
			env := env
			err := env.Enter()
			if err != nil {
				p.errExit(err)
			}
			defer func() {
				err := env.Exit()
				if err != nil && p.failed.err == nil {
					p.errExit(err)
				}
			}()
		}

		p.callstack = append(p.callstack, name)
		p.setLabels()
		ins := make([]reflect.Value, len(inputs)+1)
		ins[0] = reflect.ValueOf(ctx)
		for i, v := range inputs {
			ins[i+1] = reflect.ValueOf(v)
		}
		outs := reflect.ValueOf(f).Call(ins)
		contract.Assertf(len(outs) == len(outputs),
			"internal error: This function should be typed to return the correct number of results")
		for i, v := range outs {
			outputs[i] = v.Interface()
		}
		p.callstack = append(p.callstack, "")
		p.setLabels()

		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-p.failed.done:
	}
	if p.failed.err != nil {
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
	p.failed.err = err.(error)
	close(p.failed.done)
	runtime.Goexit()
}
