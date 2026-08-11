package step_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestWithCwd(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte("abc"), 0600)
	require.NoError(t, err)
	err = step.Pipeline("test", func(ctx context.Context) {
		step.WithCwd(ctx, dir, func(ctx context.Context) {
			f, err := os.ReadFile("text.txt")
			require.NoError(t, err)
			assert.Equal(t, f, []byte("abc"))

			s := step.ReadFile(ctx, "text.txt")
			assert.Equal(t, "abc", s)

			pwd := strings.TrimSpace(step.Cmd(ctx, "pwd"))
			assert.True(t, strings.HasSuffix(pwd, dir))
		})
	})

	assert.NoError(t, err)
}

// TestCmdGitNonInteractive is an integration-level check that step.Cmd wires
// gitenv.NonInteractive into `git` invocations specifically (not other
// commands). The exhaustive behavior of gitenv.NonInteractive itself
// (defaults, per-variable overrides, legacy GIT_SSH, core.sshCommand, etc.)
// is covered by TestNonInteractive in package gitenv, which tests that logic
// directly against explicit env slices instead of spawning subprocesses.
func TestCmdGitNonInteractive(t *testing.T) {
	// git is not guaranteed to exist in every test environment, but we only
	// need a program that prints its environment, so fall back to invoking
	// the shell's `env` builtin under the "git" name via a wrapper on PATH.
	dir := t.TempDir()
	// Fail `git config ...` (as real git does when a key is unset) so that
	// gitenv's own `git config --get core.sshCommand` lookup doesn't
	// recursively invoke this same stub and misinterpret its env dump as a
	// configured core.sshCommand value.
	gitStub := filepath.Join(dir, "git")
	err := os.WriteFile(gitStub,
		[]byte("#!/bin/sh\ncase \"$1\" in\nconfig) exit 1 ;;\nesac\nenv\n"), 0700)
	require.NoError(t, err)

	// Ensure the ambient environment doesn't already define these, so the
	// presence of the defaults below is actually due to step.Cmd and not
	// coincidental to the environment the test happens to run in.
	unsetenv(t, "GIT_TERMINAL_PROMPT")
	unsetenv(t, "GIT_SSH_COMMAND")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	var out string
	err = step.Pipeline("test", func(ctx context.Context) {
		out = step.Cmd(ctx, "git", "status")
	})
	require.NoError(t, err)
	assert.Contains(t, out, "GIT_TERMINAL_PROMPT=0\n")
	assert.Contains(t, out, "GIT_SSH_COMMAND=ssh -oBatchMode=yes\n")
}

// unsetenv unsets the named environment variable for the duration of the
// test, restoring its original value (or absence) afterwards. It exists
// because t.Setenv can only assign a value, not remove one.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	if had {
		t.Cleanup(func() { require.NoError(t, os.Setenv(key, orig)) })
	}
}

func TestEnvScoping(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	dir3 := "dir3" // This is relative to dir2
	target := filepath.Join(dir2, dir3)
	err := os.Mkdir(target, 0700)
	require.NoError(t, err)

	checkPwd := func(t *testing.T, ctx context.Context, target string) {
		pwd := step.Cmd(ctx, "pwd")
		t.Logf("PWD: %q", pwd)
		t.Logf("Target: %q", target)
		assert.True(t, strings.HasSuffix(pwd, target+"\n"))
	}

	t.Run("raw", func(t *testing.T) {
		err = step.Pipeline("test", func(ctx context.Context) {
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir1})
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir2}, &step.SetCwd{To: dir3})

			checkPwd(t, ctx, target)
		})
	})

	t.Run("scoped", func(t *testing.T) {
		err = step.Pipeline("test", func(ctx context.Context) {
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir1})
			step.WithCwd(ctx, dir2, func(ctx context.Context) {
				checkPwd(t, ctx, dir2)
			})
			checkPwd(t, ctx, dir1)
		})
	})
}
