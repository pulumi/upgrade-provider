package step

import (
	"context"
	"fmt"
	"os"
)

// A contextual environment that will exist for the scope of the call.
//
// Enter and Exit will be called an even number of times.
type Env interface {
	fmt.Stringer
	// Enter is called when a new Call occurs within the scope of the Env.
	Enter(StepInfo) error
	// Exit is called after a Call has exited within the scope of the context.
	Exit([]any) error
}

type StepInfo struct {
	name   string
	inputs []any
}

// The name of the step being executed.
func (s StepInfo) Name() string { return s.name }

// The inputs to the step being executed.
func (s StepInfo) Inputs() []any { return s.inputs }

type envKey struct{}

// Apply an Env to a context.
func WithEnv(ctx context.Context, env ...Env) context.Context {
	return context.WithValue(ctx, envKey{}, append(getEnvs(ctx), env...))
}

func getEnvs(ctx context.Context) []Env {
	envs := ctx.Value(envKey{})
	if envs, ok := envs.([]Env); ok {
		return envs
	}
	return nil
}

// Modify a environmental variable for the duration of an Env
type EnvVar struct {
	Key     string
	Value   string
	restore string
	depth   int
}

func (e *EnvVar) Enter(StepInfo) error {
	e.depth++
	if e.depth == 1 {
		e.restore = os.Getenv(e.Key)
		return os.Setenv(e.Key, e.Value)
	}
	return nil
}

func (e *EnvVar) Exit([]any) error {
	e.depth--
	if e.depth > 0 {
		return nil
	}
	if e.restore == "" {
		return os.Unsetenv(e.Key)
	}
	return os.Setenv(e.Key, e.restore)
}

func (e *EnvVar) String() string { return fmt.Sprintf("%s=%s", e.Key, e.Value) }

// Modify the current working directory for the duration of the Env.
type Cwd struct {
	To, restore string
	depth       int
}

func (e *Cwd) Enter(StepInfo) error {
	e.depth++
	if e.depth != 1 {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	e.restore = cwd
	return os.Chdir(e.To)
}

func (e *Cwd) Exit([]any) error {
	defer func() { e.depth-- }()
	if e.depth == 1 {
		return os.Chdir(e.restore)
	}
	return nil
}

func (e *Cwd) String() string { return fmt.Sprintf("cd %q", e.To) }
