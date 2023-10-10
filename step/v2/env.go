package step

import (
	"context"
	"fmt"
	"os"
)

// A contextual environment that will exist for the scope of the call.
type Env interface {
	fmt.Stringer
	Enter(StepInfo) error
	Exit([]any) error
}

type StepInfo struct {
	name   string
	inputs []any
}

func (s StepInfo) Name() string  { return s.name }
func (s StepInfo) Inputs() []any { return s.inputs }

type envKey struct{}

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

type EnvVar struct {
	Key     string
	Value   string
	restore string
	stack   int
}

func (e *EnvVar) Enter(StepInfo) error {
	e.stack++
	if e.stack == 1 {
		e.restore = os.Getenv(e.Key)
		return os.Setenv(e.Key, e.Value)
	}
	return nil
}

func (e *EnvVar) Exit([]any) error {
	e.stack--
	if e.stack > 0 {
		return nil
	}
	if e.restore == "" {
		return os.Unsetenv(e.Key)
	}
	return os.Setenv(e.Key, e.restore)
}

func (e *EnvVar) String() string { return fmt.Sprintf("%s=%s", e.Key, e.Value) }
