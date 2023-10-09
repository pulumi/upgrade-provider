package step

import (
	"context"
	"fmt"
	"os"
)

// A contextual environment that will exist for the scope of the call.
type Env interface {
	Enter() error
	Exit() error
	Display() string
}

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

func popEnvs(ctx context.Context) (context.Context, []Env) {
	envs := getEnvs(ctx)
	return context.WithValue(ctx, envKey{}, nil), envs
}

type EnvVar struct {
	Key     string
	Value   string
	restore string
}

func (e *EnvVar) Enter() error {
	e.restore = os.Getenv(e.Key)
	return os.Setenv(e.Key, e.Value)
}

func (e *EnvVar) Exit() error {
	if e.restore == "" {
		return os.Unsetenv(e.Key)
	}
	return os.Setenv(e.Key, e.restore)
}

func (e *EnvVar) Display() string {
	return fmt.Sprintf("%s=%s", e.Key, e.Value)
}
