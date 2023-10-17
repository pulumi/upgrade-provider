// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

//go:generate go run ./generate/calls.go

type pipeline struct {
	title     string
	callstack []string
	failed    error
	spinner   *spinner.Spinner
	labels    []string
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

func PipelineCtx(ctx context.Context, name string, steps func(context.Context)) (err error) {
	if getPipeline(ctx) != nil {
		panic("Cannot call pipeline when already in a pipeline")
	}

	p := &pipeline{
		title: name,
		spinner: spinner.New([]string{"|", "/", "-", "\\"},
			time.Millisecond*250,
			spinner.WithHiddenCursor(true),
		),
	}

	// If we are initially silent, don't bother to create and start a spinner
	var silent bool
	for _, env := range getEnvs(ctx) {
		_, silent = env.(*Silent)
		if silent {
			break
		}
	}

	if silent {
		p.spinner.Writer = io.Discard
	}

	p.setDisplay(getEnvs(ctx))
	done := make(chan struct{})
	go func() {
		defer func() { close(done) }()
		p.spinner.Start()
		steps(withPipeline(ctx, p))
	}()
	<-done
	p.setDisplay(getEnvs(ctx))
	if p.failed == nil {
		p.spinner.FinalMSG = fmt.Sprintf("%s--- done ---\n", p.spinner.Prefix)
	} else {
		p.spinner.FinalMSG = fmt.Sprintf("%s--- failed: %s ---\n",
			p.spinner.Prefix, p.failed.Error())
	}
	p.spinner.Stop()
	return p.failed
}

func mustGetPipeline(ctx context.Context, name string) *pipeline {
	p := getPipeline(ctx)
	if p == nil {
		panic(`Must call "` + name + `" on a context from a step function`)
	}
	return p
}

func SetLabel(ctx context.Context, label string) {
	p := mustGetPipeline(ctx, "SetLabel")
	current := p.currentFrame()
	for len(p.labels) <= current {
		p.labels = append(p.labels, "")
	}
	p.labels[current] = label
	p.setDisplay(getEnvs(ctx))
}

func (p *pipeline) setDisplay(envs []Env) {
	prefix := "--- " + p.title + " --- \n"
	prefix += p.callTree()
	opts := []string{"|", "/", "-", "\\"}
	frame := p.currentFrame()
	name := "main"
	if frame >= 0 {
		name = p.callstack[frame]
	}
	var envDisplay string
	for _, v := range envs {
		envDisplay += "\n" + v.String()
	}
	for i, o := range opts {
		opts[i] = fmt.Sprintf("%s [%s]%s", o, name, envDisplay)
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
		prefix := strings.Repeat(" ", indent-2) + "- "
		if current == i {
			if p.failed == nil {
				prefix = prefix[:indent-3] + "-> "
			} else {
				prefix = prefix[:indent-2] + "X "
			}
		}
		tree.WriteString(prefix)
		tree.WriteString(v)
		if i < len(p.labels) && p.labels[i] != "" {
			tree.WriteString(": ")
			tree.WriteString(p.labels[i])
		}
		tree.WriteRune('\n')
	}
	return tree.String()
}

// Run a function against arguments and set outputs.
func run(ctx context.Context, name string, f any, inputs, outputs []any) {
	p := mustGetPipeline(ctx, "Call("+name+")")
	done := make(chan struct{})
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
			defer func() {
				err := env.Exit(ctx, outputs)
				if err != nil && p.failed == nil {
					p.errExit(err)
				}
			}()
		}

		// If we have a silent function, disable the spinner
		if silent {
			p.spinner.Disable()
			defer p.spinner.Enable()
		}

		p.callstack = append(p.callstack, name)
		p.setDisplay(envs)
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
	if p.failed == nil {
		p.callstack = append(p.callstack, "")
	}
	p.setDisplay(getEnvs(ctx))

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
