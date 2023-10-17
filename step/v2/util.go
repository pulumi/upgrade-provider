package step

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Name a value so it is viable to the user.
func NamedValue[T any](ctx context.Context, name string, value T) T {
	return Call01(ctx, name, func(context.Context) T {
		SetLabel(ctx, fmt.Sprintf("%v", value))
		return value
	})
}

// Read a file, halting the pipeline on error.
func ReadFile(ctx context.Context, path string) string {
	return Call11E(ctx, path, func(ctx context.Context, path string) (string, error) {
		MarkImpure(ctx)
		bytes, err := os.ReadFile(path)
		SetLabel(ctx, fmt.Sprintf("%d bytes read", len(bytes)))
		return string(bytes), err
	}, path)
}

// Write a file, halting the pipeline on error.
func WriteFile(ctx context.Context, path, content string) {
	Call20E(ctx, path, func(ctx context.Context, path, content string) error {
		MarkImpure(ctx)
		return os.WriteFile(path, []byte(content), 0600)
	}, path, content)
}

// Run a shell command, halting the pipeline on error.
func Cmd(ctx context.Context, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	key := cmd.String()
	return Call11E(ctx, name, func(ctx context.Context, _ string) (string, error) {
		MarkImpure(ctx)
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return string(out), err
	}, key)
}

// Halt the pipeline if err is non-nil.
func HaltOnError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	Call00E(ctx, err.Error(),
		func(context.Context) error {
			return err
		})
}
