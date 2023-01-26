package main

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/briandowns/spinner"
)

type DeferredStep struct {
	description string
	f           func() (string, error)
}

func (ds DeferredStep) run() bool {
	options := []string{"|", "/", "-", "\\", "-"}
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

// Display a step to the user.
func Step(description string, step func() (string, error)) DeferredStep {
	return DeferredStep{
		description: description,
		f:           step,
	}
}

func RunSteps(description string, steps ...DeferredStep) bool {
	fmt.Printf("---- %s ----\n", description)
	for _, step := range steps {
		if !step.run() {
			return false
		}
	}
	return true
}

func CommandStep(command *exec.Cmd) DeferredStep {
	return Step(command.String(), func() (string, error) {
		return "", command.Run()
	})
}

func (s DeferredStep) AssignTo(position *string) DeferredStep {
	return DeferredStep{
		description: s.description,
		f: func() (string, error) {
			r, err := s.f()
			*position = r
			return r, err
		},
	}
}
