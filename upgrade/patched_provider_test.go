package upgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatchedProviderUpgradeCommands(t *testing.T) {
	t.Parallel()

	assert.Equal(t, [][]string{
		{"git", "submodule", "update", "--force", "--init", "--", "upstream"},
		{"git", "-C", "upstream", "fetch", "--tags"},
		{"./scripts/upstream.sh", "checkout"},
		{"./scripts/upstream.sh", "rebase", "-o", "refs/tags/v1.2.3"},
		{"./scripts/upstream.sh", "check_in"},
	}, patchedProviderUpgradeCommands("refs/tags/v1.2.3"))
}

func TestCheckPatchedProviderPreflight(t *testing.T) {
	t.Parallel()

	t.Run("uninitialized submodule", func(t *testing.T) {
		t.Parallel()
		upstream := t.TempDir()

		result, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.NoError(t, err)
		assert.Contains(t, result, "not yet initialized")
	})

	t.Run("normal detached state", func(t *testing.T) {
		t.Parallel()
		upstream := newPatchedTestRepo(t)
		runPatchedTestGit(t, upstream, "checkout", "--detach")

		result, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.NoError(t, err)
		assert.Equal(t, "ready", result)
	})

	t.Run("patch checkout requires manual recovery", func(t *testing.T) {
		t.Parallel()
		upstream := newPatchedTestRepo(t)
		runPatchedTestGit(t, upstream, "checkout", "-b", patchCheckoutBranch)
		before := runPatchedTestGit(t, upstream, "rev-parse", "HEAD")

		_, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.ErrorContains(t, err, patchCheckoutBranch)
		require.ErrorContains(t, err, "./scripts/upstream.sh rebase -o refs/tags/v1.2.3")
		require.ErrorContains(t, err, "./scripts/upstream.sh check_in")
		require.ErrorContains(t, err, "destructive")
		assert.Equal(t, patchCheckoutBranch,
			strings.TrimSpace(runPatchedTestGit(t, upstream, "branch", "--show-current")))
		assert.Equal(t, before, runPatchedTestGit(t, upstream, "rev-parse", "HEAD"))
	})

	t.Run("active operation stops before branch recovery", func(t *testing.T) {
		t.Parallel()
		upstream := newPatchedTestRepo(t)
		runPatchedTestGit(t, upstream, "checkout", "-b", patchCheckoutBranch)
		gitDir := strings.TrimSpace(runPatchedTestGit(t, upstream, "rev-parse", "--absolute-git-dir"))
		require.NoError(t, os.Mkdir(filepath.Join(gitDir, "rebase-merge"), 0o700))

		_, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.ErrorContains(t, err, "active Git operation")
		require.ErrorContains(t, err, "upgrade-provider left it unchanged")
		require.ErrorContains(t, err, "./scripts/upstream.sh check_in")
	})

	t.Run("unexpected branch is preserved", func(t *testing.T) {
		t.Parallel()
		upstream := newPatchedTestRepo(t)
		runPatchedTestGit(t, upstream, "checkout", "-b", "feature")

		_, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.ErrorContains(t, err, `unexpected branch "feature"`)
		assert.Equal(t, "feature", strings.TrimSpace(runPatchedTestGit(t, upstream, "branch", "--show-current")))
	})

	t.Run("non-repository contents are preserved", func(t *testing.T) {
		t.Parallel()
		upstream := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(upstream, "work.txt"), []byte("keep"), 0o600))

		_, err := checkPatchedProviderPreflight(
			context.Background(), upstream, "refs/tags/v1.2.3")

		require.ErrorContains(t, err, "not an initialized Git repository but contains files")
		content, readErr := os.ReadFile(filepath.Join(upstream, "work.txt"))
		require.NoError(t, readErr)
		assert.Equal(t, "keep", string(content))
	})
}

func newPatchedTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runPatchedTestGit(t, dir, "init")
	runPatchedTestGit(t, dir, "config", "user.name", "Upgrade Provider Test")
	runPatchedTestGit(t, dir, "config", "user.email", "upgrade-provider@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "provider.txt"), []byte("upstream\n"), 0o600))
	runPatchedTestGit(t, dir, "add", "provider.txt")
	runPatchedTestGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runPatchedTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s failed: %s", strings.Join(args, " "), output)
	return string(output)
}
