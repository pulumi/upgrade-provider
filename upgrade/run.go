package upgrade

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/briandowns/spinner"
)

type RunArgs struct {
	Folder string
	Cmd    []string
}

type Runner interface {
	Run(cmd []string) string
	RunInFolder(folder string, cmd []string) string
	GetCwd(ctx context.Context) string
}

var _ Runner = &DefaultRunner{}

type DefaultRunner struct{}

func runWithSpinner(description string, f func() (string, error)) (string, error) {
	options := []string{"|", "/", "-", "\\"}
	for i, o := range options {
		options[i] = o + " " + description
	}
	spinner := spinner.New(options, time.Millisecond*250,
		spinner.WithHiddenCursor(true))
	spinner.Start()
	result, err := f()
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
	fmt.Printf(" %s: %s\n", description, result)
	return result, err
}

func (r *DefaultRunner) Run(cmd []string) string {
	return r.RunInFolder("", cmd)
}

func (r *DefaultRunner) RunInFolder(folder string, cmd []string) string {
	cm := exec.Command(cmd[0], cmd[1:]...)
	if folder != "" {
		cm.Dir = folder
	}

	description := strings.Join(cmd, " ")
	if len(description) > 80 {
		description = description[:80] + "..."
	}

	result, err := runWithSpinner(description, func() (string, error) {
		out, err := cm.Output()
		output := string(out)
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return output, err
	})
	if err != nil {
		fmt.Printf(" %s failed: %s\n", description, err.Error())
		fmt.Println(result)
		panic(err)
	}
	return result
}

func (r *DefaultRunner) GetCwd(ctx context.Context) string {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("failed to get current working directory: %s\n", err.Error()))
	}
	return wd
}
