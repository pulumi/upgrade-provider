package step

import (
	"context"
	"os/exec"
)

func NamedValue[T any](ctx context.Context, name string, value T) T {
	return Call01(ctx, name, func(context.Context) T { return value })
}

func Cmd(ctx context.Context, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	key := cmd.String()
	return Call11E(ctx, key, func(ctx context.Context, _ string) (string, error) {
		out, err := cmd.Output()
		return string(out), err
	}, key)
}
