package step

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Name a value so it is viable to the user.
func NamedValue[T any](ctx context.Context, name string, value T) T {
	return Func01(name, func(context.Context) T {
		SetLabel(ctx, fmt.Sprintf("%v", value))
		return value
	})(ctx)
}

// Read a file, halting the pipeline on error.
func ReadFile(ctx context.Context, path string) string {
	return Func11E(path, func(ctx context.Context, path string) (string, error) {
		MarkImpure(ctx)
		bytes, err := os.ReadFile(path)
		SetLabel(ctx, fmt.Sprintf("%d bytes read", len(bytes)))
		return string(bytes), err
	})(ctx, path)
}

// Write a file, halting the pipeline on error.
func WriteFile(ctx context.Context, path, content string) {
	Func20E(path, func(ctx context.Context, path, content string) error {
		MarkImpure(ctx)
		return os.WriteFile(path, []byte(content), 0600)
	})(ctx, path, content)
}

// Run a shell command, halting the pipeline on error.
func Cmd(ctx context.Context, name string, args ...string) string {
	return Func21E(name, func(ctx context.Context, _ string, _ []string) (string, error) {
		MarkImpure(ctx)
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return string(out), err
	})(ctx, name, args)
}

// Halt the pipeline if err is non-nil.
func HaltOnError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	Func00E(err.Error(), func(context.Context) error {
		return err
	})(ctx)
}
