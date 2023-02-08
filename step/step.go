// Step is a library for building user facing pipelines.
//
// Step is responsible for presenting each step in a job to the user, and halting the
// pipeline on an error.
package step

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/briandowns/spinner"
)

// A Step represents an atomic (pass/fail) piece of computation that should be displayed
// to the user.
type Step interface {
	In(path *string) Step
	AssignTo(position *string) Step
	run() bool
}

type step struct {
	description string
	f           func() (string, error)
}

func (ds step) run() bool {
	options := []string{"|", "/", "-", "\\"}
	for i, o := range options {
		options[i] = o + " " + ds.description
	}
	spinner := spinner.New(options, time.Millisecond*250,
		spinner.WithHiddenCursor(true))
	spinner.Start()
	result, err := ds.f()
	if err != nil {
		spinner.FinalMSG = "X"
		result = err.Error()
	} else {
		spinner.FinalMSG = "✓"
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

// Run a series of steps with an under a name.
//
// Each step is run in order. If all steps ran without errors, true is returned. If any
// step returned an error, false is returned.
func RunJob(description string, steps ...Step) bool {
	fmt.Printf("---- %s ----\n", description)
	for _, step := range steps {
		if !step.run() {
			return false
		}
	}
	return true
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
	return step{
		description: s.description,
		f: func() (result string, err error) {
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
			result, err = s.f()
			return result, err
		},
	}
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
func (us unknownStep) run() bool {
	return us.f().run()
}
