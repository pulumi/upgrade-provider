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
	// Run the command in the directory
	In(path *string) Step
	// Assign the output of the commend to the location.
	AssignTo(lvalue *string) Step
	// Override the output of the command, and assign the rvalue
	Return(rvalue *string) Step
	run(prefix string) bool
}

type step struct {
	description string
	f           func() (string, error)
	path        *string
	rvalue      *string
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

func (ds step) Return(rvalue *string) Step {
	ds.rvalue = rvalue
	return ds
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
	var output string
	description := command.String()
	if len(description) > 80 {
		description = description[:80] + "..."
	}
	return F(description, func() (string, error) {
		out, err := command.Output()
		output = string(out)
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return "", err
	}).Return(&output)
}

// Set an environmental variable.
func Env(key, value string) Step {
	return F(fmt.Sprintf("%s=%q", key, value), func() (string, error) {
		return "", os.Setenv(key, value)
	})
}

// Assign the output value of the step a variable.
func (s step) AssignTo(position *string) Step {
	return step{
		description: s.description,
		path:        s.path,
		rvalue:      s.rvalue,
		f: func() (string, error) {
			r, err := s.f()
			if s.rvalue != nil {
				*position = *s.rvalue
			} else {
				*position = r
			}
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
			s := us.f()
			if s == nil {
				return nil
			}
			return s.In(path)
		},
	}
}
func (us unknownStep) AssignTo(lvalue *string) Step {
	return unknownStep{
		f: func() Step {
			s := us.f()
			if s == nil {
				return nil
			}
			return s.AssignTo(lvalue)
		},
	}
}

func (us unknownStep) Return(rvalue *string) Step {
	return unknownStep{
		f: func() Step {
			s := us.f()
			if s == nil {
				return nil
			}
			return s.Return(rvalue)
		},
	}
}

func (us unknownStep) run(prefix string) bool {
	s := us.f()
	if s == nil {
		return true
	}
	return s.run(prefix)
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
	rvalue   *string
}

func (c combined) Return(rvalue *string) Step {
	c.rvalue = rvalue
	return c
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
		if s == nil {
			continue
		}
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
	if c.assignTo != nil && c.rvalue != nil {
		*c.assignTo = *c.rvalue
	}
	return true
}

// Run a step, returning if the step succeeded.
func Run(step Step) bool {
	if step == nil {
		return true
	}
	return step.run("")
}
