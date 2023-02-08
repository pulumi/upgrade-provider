// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/briandowns/spinner"
)

// A Step represents an atomic (pass/fail) piece of computation that should be displayed
// to the user.
type Step interface {
	In(path *string) Step
	AssignTo(position *string) Step
	run(prefix string) bool
}

type step struct {
	description string
	f           func() (string, error)
	path        *string
}

func (ds step) run(prefix string) bool {
	options := []string{"|", "/", "-", "\\"}
	for i, o := range options {
		options[i] = prefix + o + " " + ds.description
	}
	spinner := spinner.New(options, time.Millisecond*250,
		spinner.WithHiddenCursor(true))
	spinner.Start()
	result, err := runIn(ds.path, ds.f)
	if err != nil {
		spinner.FinalMSG = prefix + "X"
		result = err.Error()
	} else {
		spinner.FinalMSG = prefix + "✓"
		if result == "" {
			result = "done"
		}
	}
	spinner.Stop()
	fmt.Printf(" %s: %s\n", ds.description, result)
	return err == nil
}

// Create a step based around a function.
func F(description string, action func() (string, error)) Step {
	return step{
		description: description,
		f:           action,
	}
}

// Create a step from a *exec.Cmd.
func Cmd(command *exec.Cmd) Step {
	return F(command.String(), func() (string, error) {
		_, err := command.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return "", err
	})
}

// Assign the output value of the step a variable.
func (s step) AssignTo(position *string) Step {
	return step{
		description: s.description,
		f: func() (string, error) {
			r, err := s.f()
			*position = r
			return r, err
		},
	}
}

// Run the step in the specified directory.
func (s step) In(path *string) Step {
	if s.path == nil {
		s.path = path
	}
	return s
}

func runIn(path *string, f func() (string, error)) (string, error) {
	if path == nil {
		return f()
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	err = os.Chdir(*path)
	if err != nil {
		return "", err
	}
	defer func() {
		e := os.Chdir(wd)
		if err == nil {
			err = e
		}
	}()
	return f()
}

// A Step that can't be fully computed until it is run. This allows constructing a Step
// that depends on information gathered in other steps.
func Computed(step func() Step) Step {
	return unknownStep{f: step}
}

type unknownStep struct {
	f func() Step
}

func (us unknownStep) In(path *string) Step {
	return unknownStep{
		f: func() Step {
			return us.f().In(path)
		},
	}
}
func (us unknownStep) AssignTo(position *string) Step {
	return unknownStep{
		f: func() Step {
			return us.f().AssignTo(position)
		},
	}
}
func (us unknownStep) run(prefix string) bool {
	return us.f().run(prefix)
}

// Run a series of steps with an under a name.
//
// Each step is run in order. If all steps ran without errors, true is returned. If any
// step returned an error, false is returned.
func Combined(description string, steps ...Step) Step {
	return combined{description: description, steps: steps}
}

type combined struct {
	description string
	steps       []Step

	path     *string
	assignTo *string
}

func (c combined) In(path *string) Step {
	c.path = path
	return c
}

func (c combined) AssignTo(position *string) Step {
	c.assignTo = position
	return c
}

func (c combined) run(prefix string) bool {
	description := prefix + c.description
	if prefix == "" {
		description = "---- " + description + " ----"
	}
	fmt.Println(description)
	subPrefix := strings.Repeat(" ", len(prefix))
	for _, s := range c.steps {
		if c.path != nil {
			s = s.In(c.path)
		}
		if c.assignTo != nil {
			s = s.AssignTo(c.assignTo)
		}
		ok := s.run(subPrefix + "- ")
		if !ok {
			return false
		}
	}
	return true
}

func Run(step Step) bool {
	return step.run("")
}
