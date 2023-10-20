package step

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/briandowns/spinner"
)

// A display shows an executing pipeline to the user.
type Display interface {
	// Initialize the display.
	//
	// At this point, the Display can assume that it is the only entity that will draw on the screen.
	Start(ctx context.Context, title string) error

	// Set a label on the currently running task
	SetLabel(ctx context.Context, label string) error

	EnterStep(ctx context.Context, name string) error
	ExitStep(ctx context.Context, success bool) error

	// Draw the current state to the screen.
	Refresh(ctx context.Context, envs []Env) error

	Pause(ctx context.Context) error
	Resume(ctx context.Context) error

	// Finish with the display.
	//
	// No more methods will be called on the display after this.
	Finish(ctx context.Context, success bool) error
}

type spinnerDisplay struct {
	title   string
	spinner *spinner.Spinner

	callstack []string
	labels    []string
	failed    bool
}

func (s *spinnerDisplay) Start(ctx context.Context, title string) error {
	s.title = title
	s.spinner = spinner.New([]string{"|", "/", "-", "\\"},
		time.Millisecond*250,
		spinner.WithHiddenCursor(true))
	return s.Resume(ctx)
}

func (s *spinnerDisplay) SetLabel(ctx context.Context, label string) error {
	current := s.currentFrame()
	for len(s.labels) <= current {
		s.labels = append(s.labels, "")
	}
	s.labels[current] = label

	return nil
}

func (s *spinnerDisplay) Pause(context.Context) error  { s.spinner.Disable(); return nil }
func (s *spinnerDisplay) Resume(context.Context) error { s.spinner.Enable(); return nil }

func (s *spinnerDisplay) EnterStep(ctx context.Context, name string) error {
	s.callstack = append(s.callstack, name)
	return nil
}

func (s *spinnerDisplay) ExitStep(ctx context.Context, success bool) error {
	if success {
		s.callstack = append(s.callstack, "")
	}
	return nil

}

func (s *spinnerDisplay) Finish(ctx context.Context, success bool) error {
	var msg string
	if success {
		msg = "done"
	} else {
		msg = "failed"
	}
	s.spinner.FinalMSG = fmt.Sprintf("%s--- %s ---\n", s.spinner.Prefix, msg)
	s.spinner.Stop()
	return nil
}

func DefaultDisplay(opts *options) {
	opts.display = &spinnerDisplay{}
}

func (p *spinnerDisplay) Refresh(_ context.Context, envs []Env) error {
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

	return nil
}

func (p *spinnerDisplay) callTree() string {
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
			if p.failed {
				prefix = prefix[:indent-2] + "X "
			} else {
				prefix = prefix[:indent-3] + "-> "
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

func (p *spinnerDisplay) currentFrame() int {
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

// A display that doesn't display anything.
func NullDisplay(opts *options) {
	opts.display = nullDisplayType{}
}

var nullDisplay = nullDisplayType{}

type nullDisplayType struct{}

func (nullDisplayType) Start(ctx context.Context, title string) error    { return nil }
func (nullDisplayType) SetLabel(ctx context.Context, label string) error { return nil }
func (nullDisplayType) EnterStep(ctx context.Context, name string) error { return nil }
func (nullDisplayType) ExitStep(ctx context.Context, success bool) error { return nil }
func (nullDisplayType) Refresh(ctx context.Context, envs []Env) error    { return nil }
func (nullDisplayType) Finish(ctx context.Context, success bool) error   { return nil }
func (nullDisplayType) Pause(context.Context) error                      { return nil }
func (nullDisplayType) Resume(context.Context) error                     { return nil }
