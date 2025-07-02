package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

var _ Runner = &TestRunner{}

type RunResult struct {
	Output string
	Error  error
}

type TestRunner struct {
	mockMap map[string]RunResult
	wd      string
}

func (r *TestRunner) Run(cmd []string) string {
	return r.RunInFolder("", cmd)
}

func (r *TestRunner) RunInFolder(folder string, cmd []string) string {
	result, ok := r.mockMap[strings.Join(cmd, " ")]
	if !ok {
		fmt.Printf("command not found: %s\n", cmd)
		runtime.Goexit()
	}
	return result.Output
}

func (r *TestRunner) GetCwd(ctx context.Context) string {
	return r.wd
}

func NewTestRunnerFromFile(path string) (*TestRunner, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	mockMap := make(map[string]RunResult)
	err = json.Unmarshal(bytes, &mockMap)
	if err != nil {
		return nil, err
	}
	return &TestRunner{mockMap: mockMap}, nil
}
