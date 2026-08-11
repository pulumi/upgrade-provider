package step

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandEnvironment(t *testing.T) {
	t.Setenv("UPGRADE_PROVIDER_STEP_TEST", "parent")

	var output string
	ctx := WithCommandEnv(context.Background(), map[string]string{
		"UPGRADE_PROVIDER_STEP_TEST": "child",
	})
	ok := Run(ctx, Cmd("sh", "-c", "printf %s \"$UPGRADE_PROVIDER_STEP_TEST\"").AssignTo(&output))

	require.True(t, ok)
	assert.Equal(t, "child", output)
	assert.Equal(t, "parent", os.Getenv("UPGRADE_PROVIDER_STEP_TEST"))
}

func TestCommandEnvironmentNestedOverride(t *testing.T) {
	ctx := WithCommandEnv(context.Background(), map[string]string{
		"UPGRADE_PROVIDER_STEP_FIRST":  "first",
		"UPGRADE_PROVIDER_STEP_SHARED": "outer",
	})
	ctx = WithCommandEnv(ctx, map[string]string{
		"UPGRADE_PROVIDER_STEP_SECOND": "second",
		"UPGRADE_PROVIDER_STEP_SHARED": "inner",
	})

	var output string
	ok := Run(ctx, Cmd("sh", "-c", `printf "%s|%s|%s" "$UPGRADE_PROVIDER_STEP_FIRST" "$UPGRADE_PROVIDER_STEP_SECOND" "$UPGRADE_PROVIDER_STEP_SHARED"`).AssignTo(&output))

	require.True(t, ok)
	assert.Equal(t, "first|second|inner", output)
}

// TestCmdGitNonInteractive guards against the network-touching git commands
// issued through this legacy Cmd (e.g. from the patched-provider upgrade
// workflow) regressing back to hanging on an interactive prompt. See
// https://github.com/pulumi/upgrade-provider/issues/138.
//
// The exhaustive behavior of the non-interactive defaults themselves
// (per-variable overrides, legacy GIT_SSH, core.sshCommand, etc.) is covered
// by TestNonInteractive in package gitenv.
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
	require.NoError(t, os.WriteFile(gitStub,
		[]byte("#!/bin/sh\ncase \"$1\" in\nconfig) exit 1 ;;\nesac\nenv\n"), 0700))

	// Ensure the ambient environment doesn't already define these, so the
	// presence of the defaults below is actually due to Cmd and not
	// coincidental to the environment the test happens to run in.
	unsetenvForTest(t, "GIT_TERMINAL_PROMPT")
	unsetenvForTest(t, "GIT_SSH_COMMAND")

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	var output string
	ok := Run(context.Background(), Cmd("git", "status").AssignTo(&output))

	require.True(t, ok)
	assert.Contains(t, output, "GIT_TERMINAL_PROMPT=0\n")
	assert.Contains(t, output, "GIT_SSH_COMMAND=ssh -oBatchMode=yes\n")
}

// unsetenvForTest unsets the named environment variable for the duration of
// the test, restoring its original value (or absence) afterwards. It exists
// because t.Setenv can only assign a value, not remove one.
func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	orig, had := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	if had {
		t.Cleanup(func() { require.NoError(t, os.Setenv(key, orig)) })
	}
}
