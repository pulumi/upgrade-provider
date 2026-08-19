package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonInteractive(t *testing.T) {
	t.Run("sets both defaults when neither is configured", func(t *testing.T) {
		env := NonInteractive(context.Background(), []string{"UNRELATED=1"})
		assert.Contains(t, env, "UNRELATED=1")
		assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
		assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	})

	t.Run("respects both caller-provided overrides", func(t *testing.T) {
		env := NonInteractive(context.Background(), []string{
			"GIT_TERMINAL_PROMPT=1",
			"GIT_SSH_COMMAND=ssh -vvv",
		})
		assert.Equal(t, []string{
			"GIT_TERMINAL_PROMPT=1",
			"GIT_SSH_COMMAND=ssh -vvv",
		}, env)
	})

	t.Run("respects only GIT_TERMINAL_PROMPT override, still defaults GIT_SSH_COMMAND", func(t *testing.T) {
		env := NonInteractive(context.Background(), []string{"GIT_TERMINAL_PROMPT=1"})
		assert.Contains(t, env, "GIT_TERMINAL_PROMPT=1")
		assert.NotContains(t, env, "GIT_TERMINAL_PROMPT=0")
		assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	})

	t.Run("respects only GIT_SSH_COMMAND override, still defaults GIT_TERMINAL_PROMPT", func(t *testing.T) {
		env := NonInteractive(context.Background(), []string{"GIT_SSH_COMMAND=ssh -vvv"})
		assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
		assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -vvv")
		assert.NotContains(t, env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	})

	t.Run("leaves legacy GIT_SSH alone instead of overriding it", func(t *testing.T) {
		env := NonInteractive(context.Background(), []string{"GIT_SSH=/usr/bin/custom-ssh"})
		assert.Contains(t, env, "GIT_SSH=/usr/bin/custom-ssh")
		assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
		for _, kv := range env {
			assert.False(t, strings.HasPrefix(kv, "GIT_SSH_COMMAND="),
				"GIT_SSH_COMMAND should not be set when legacy GIT_SSH is configured, got %q", kv)
		}
	})

	t.Run("uses os.Environ() as the base when env is nil", func(t *testing.T) {
		t.Setenv("UPGRADE_PROVIDER_GITENV_TEST_MARKER", "present")
		env := NonInteractive(context.Background(), nil)
		assert.Contains(t, env, "UPGRADE_PROVIDER_GITENV_TEST_MARKER=present")
		assert.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
	})

	t.Run("prefers core.sshCommand over the bare ssh default, without dropping it", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not available on PATH")
		}

		dir := t.TempDir()
		requireGit := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		requireGit("init", "-q")
		requireGit("config", "core.sshCommand", "ssh -i /custom/key")

		wd, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(dir))
		t.Cleanup(func() { require.NoError(t, os.Chdir(wd)) })

		env := NonInteractive(context.Background(), []string{"UNRELATED=1"})
		assert.Contains(t, env, "GIT_SSH_COMMAND=ssh -i /custom/key -oBatchMode=yes")
	})
}
