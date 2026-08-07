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

func TestCmdGitNonInteractive(t *testing.T) {
	// git is not guaranteed to exist in every test environment, but we only
	// need a program that prints its environment, so fall back to invoking
	// the shell's `env` builtin under the "git" name via a wrapper on PATH.
	dir := t.TempDir()
	gitStub := filepath.Join(dir, "git")
	err := os.WriteFile(gitStub, []byte("#!/bin/sh\nenv\n"), 0700)
	require.NoError(t, err)

	withStubOnPath := func(t *testing.T, extraEnv ...string) string {
		t.Helper()
		origPath := os.Getenv("PATH")
		require.NoError(t, os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath))
		t.Cleanup(func() { require.NoError(t, os.Setenv("PATH", origPath)) })

		for _, kv := range extraEnv {
			key, value, _ := strings.Cut(kv, "=")
			orig, had := os.LookupEnv(key)
			require.NoError(t, os.Setenv(key, value))
			t.Cleanup(func() {
				if had {
					require.NoError(t, os.Setenv(key, orig))
				} else {
					require.NoError(t, os.Unsetenv(key))
				}
			})
		}

		var out string
		err := step.Pipeline("test", func(ctx context.Context) {
			out = step.Cmd(ctx, "git", "status")
		})
		require.NoError(t, err)
		return out
	}

	t.Run("sets non-interactive defaults", func(t *testing.T) {
		out := withStubOnPath(t)
		assert.Contains(t, out, "GIT_TERMINAL_PROMPT=0\n")
		assert.Contains(t, out, "GIT_SSH_COMMAND=ssh -oBatchMode=yes\n")
	})

	t.Run("respects caller-provided overrides", func(t *testing.T) {
		out := withStubOnPath(t,
			"GIT_TERMINAL_PROMPT=1",
			"GIT_SSH_COMMAND=ssh -vvv",
		)
		assert.Contains(t, out, "GIT_TERMINAL_PROMPT=1\n")
		assert.Contains(t, out, "GIT_SSH_COMMAND=ssh -vvv\n")
	})
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
