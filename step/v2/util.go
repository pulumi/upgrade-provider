package step

import (
	"context"
	"os"
	"os/exec"
)

func NamedValue[T any](ctx context.Context, name string, value T) T {
	return Call01(ctx, name, func(context.Context) T { return value })
}

func ReadFile(ctx context.Context, path string) string {
	return Call11E(ctx, path, func(ctx context.Context, path string) (string, error) {
		bytes, err := os.ReadFile(path)
		return string(bytes), err
	}, path)
}

func Cmd(ctx context.Context, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	key := cmd.String()
	return Call11E(ctx, key, func(ctx context.Context, _ string) (string, error) {
		out, err := cmd.Output()
		return string(out), err
	}, key)
}
