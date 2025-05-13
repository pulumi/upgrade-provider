package step

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
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
		cmd := exec.CommandContext(ctx, name, args...)
		SetLabel(ctx, cmd.String())
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		return string(out), err
	})(ctx, name, args)
}

// Like [Cmd] but never errors, instead reports the errors out.
func CmdIgnoreError(ctx context.Context, name string, args ...string) string {
	return Func21E(name, func(ctx context.Context, _ string, _ []string) (string, error) {
		MarkImpure(ctx)
		cmd := exec.CommandContext(ctx, name, args...)
		SetLabel(ctx, cmd.String())
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s:\n%s", err.Error(), string(exit.Stderr))
		}
		if err != nil {
			return fmt.Sprintf("ERROR while executing `%s %s`: %v\n\nOutput: %s\n\n",
				name, strings.Join(args, " "), err, out,
			), nil
		}
		return string(out), nil
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

// Run `f` in `dir`.
//
// If the dir does not exist, then the current pipeline will fail.
func WithCwd(ctx context.Context, dir string, f func(context.Context)) {
	c := &SetCwd{To: dir}
	err := c.Enter(ctx, StepInfo{})
	HaltOnError(ctx, err)
	c.depth--

	defer func() {
		c.depth++
		err := c.Exit(ctx, nil)
		HaltOnError(ctx, err)
	}()

	f(WithEnv(ctx, c))
}

// Read the current working directory.
func GetCwd(ctx context.Context) string {
	return Func01E("GetCwd", func(ctx context.Context) (string, error) {
		MarkImpure(ctx)
		c, err := os.Getwd()
		if err == nil {
			SetLabel(ctx, c)
		}
		return c, err
	})(ctx)
}

// MkDirAll wraps os.MkdirAll with error handling and impure validation.
func MkDirAll(ctx context.Context, path string, perm os.FileMode) {
	Func20E("MkDirAll", func(ctx context.Context, path string, perm os.FileMode) error {
		MarkImpure(ctx)
		return os.MkdirAll(path, perm)
	})(ctx, path, perm)
}

// Returns FileInfo{...}, true if the file exists, or FileInfo{}, false if it does
// not. Any other error type will fail the pipeline.
func Stat(ctx context.Context, name string) (FileInfo, bool) {
	return Func12E("Stat", func(ctx context.Context, name string) (FileInfo, bool, error) {
		MarkImpure(ctx)
		info, err := os.Stat(name)
		if os.IsNotExist(err) {
			return FileInfo{}, false, nil
		}
		if err != nil {
			return FileInfo{}, false, err
		}

		return FileInfo{
			Name:  info.Name(),
			Size:  info.Size(),
			Mode:  info.Mode(),
			IsDir: info.IsDir(),
		}, true, nil
	})(ctx, name)
}

// This is a partial copy of fs.FileInfo that is fully serializable.
//
// This is necessary for replay tests.
type FileInfo struct {
	Name  string      `json:"name"`  // base name of the file
	Size  int64       `json:"size"`  // length in bytes for regular files; system-dependent for others
	Mode  fs.FileMode `json:"mode"`  // file mode bits
	IsDir bool        `json:"isDir"` // abbreviation for Mode().IsDir()
}

// GetEnv wraps os.Getenv.
//
// It correctly declares itself as an impure function, making it safe to use in replay
// pipelines.
func GetEnv(ctx context.Context, key string) string {
	return Func11("GetEnv", func(ctx context.Context, key string) string {
		MarkImpure(ctx)
		result := os.Getenv(key)
		SetLabel(ctx, key+"="+result)
		return result
	})(ctx, key)
}
