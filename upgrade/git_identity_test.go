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

	legacystep "github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

type fakeGitIdentitySource struct {
	environment map[string]string
	config      map[string]string

	configCalls int
}

func (s *fakeGitIdentitySource) LookupEnv(key string) (string, bool) {
	value, ok := s.environment[key]
	return value, ok
}

func (s *fakeGitIdentitySource) GitConfig(_ context.Context, _, key string) (string, error) {
	s.configCalls++
	return s.config[key], nil
}

func TestResolveGitIdentityFromRepositoryConfig(t *testing.T) {
	source := &fakeGitIdentitySource{
		config: map[string]string{
			"user.name":  "Configured User",
			"user.email": "configured@example.com",
		},
	}

	identity, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.NoError(t, err)
	assert.Equal(t, gitIdentity{
		AuthorName:     "Configured User",
		AuthorEmail:    "configured@example.com",
		CommitterName:  "Configured User",
		CommitterEmail: "configured@example.com",
	}, identity)
	assert.Equal(t, 6, source.configCalls)
}

func TestResolveGitIdentityHonorsAuthorAndCommitterConfigOverUser(t *testing.T) {
	source := &fakeGitIdentitySource{
		config: map[string]string{
			"user.name":       "Fallback User",
			"user.email":      "fallback@example.com",
			"author.name":     "Author Config",
			"author.email":    "author-config@example.com",
			"committer.name":  "Committer Config",
			"committer.email": "committer-config@example.com",
		},
	}

	identity, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.NoError(t, err)
	assert.Equal(t, gitIdentity{
		AuthorName:     "Author Config",
		AuthorEmail:    "author-config@example.com",
		CommitterName:  "Committer Config",
		CommitterEmail: "committer-config@example.com",
	}, identity)
}

func TestResolveGitIdentityFillsMissingAuthorOrCommitterFieldsFromUser(t *testing.T) {
	source := &fakeGitIdentitySource{
		config: map[string]string{
			"user.name":    "Fallback User",
			"user.email":   "fallback@example.com",
			"author.name":  "Author Config",
			"author.email": "author-config@example.com",
		},
	}

	identity, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.NoError(t, err)
	assert.Equal(t, gitIdentity{
		AuthorName:     "Author Config",
		AuthorEmail:    "author-config@example.com",
		CommitterName:  "Fallback User",
		CommitterEmail: "fallback@example.com",
	}, identity)
}

func TestResolveGitIdentityExplicitEnvironmentTakesPrecedence(t *testing.T) {
	source := &fakeGitIdentitySource{
		environment: map[string]string{
			gitAuthorName:     "Explicit Author",
			gitAuthorEmail:    "author@example.com",
			gitCommitterName:  "Explicit Committer",
			gitCommitterEmail: "committer@example.com",
		},
		config: map[string]string{"user.name": "Configured", "user.email": "configured@example.com"},
	}

	identity, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.NoError(t, err)
	assert.Equal(t, "Explicit Author", identity.AuthorName)
	assert.Equal(t, "author@example.com", identity.AuthorEmail)
	assert.Equal(t, "Explicit Committer", identity.CommitterName)
	assert.Equal(t, "committer@example.com", identity.CommitterEmail)
	assert.Zero(t, source.configCalls)
}

func TestResolveGitIdentityFillsPartialEnvironmentByPrecedence(t *testing.T) {
	source := &fakeGitIdentitySource{
		environment: map[string]string{
			gitAuthorName: "Explicit Author",
		},
		config: map[string]string{
			"user.name":  "Configured User",
			"user.email": "configured@example.com",
		},
	}

	identity, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.NoError(t, err)
	assert.Equal(t, gitIdentity{
		AuthorName:     "Explicit Author",
		AuthorEmail:    "configured@example.com",
		CommitterName:  "Configured User",
		CommitterEmail: "configured@example.com",
	}, identity)
}

func TestResolveGitIdentityFailureIsActionable(t *testing.T) {
	source := &fakeGitIdentitySource{config: map[string]string{}}

	_, err := resolveGitIdentity(context.Background(), "/provider", source)

	require.Error(t, err)
	assert.ErrorContains(t, err, "Git author and committer identity is required to run the patched-provider workflow")
	assert.ErrorContains(t, err, gitAuthorName)
	assert.ErrorContains(t, err, `git -C "/provider" config user.name`)
	assert.ErrorContains(t, err, "Global Git configuration is not required")
}

func TestGitIdentityPreflightFailureIsReadOnly(t *testing.T) {
	root := t.TempDir()
	runGitTestCommand(t, root, "init")
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("unchanged\n"), 0o600))
	runGitTestCommand(t, root, "add", "README.md")
	runGitTestCommand(t, root, "-c", "user.name=Initial Author", "-c", "user.email=initial@example.com", "commit", "-m", "initial")

	for _, key := range gitIdentityEnvironmentKeys() {
		t.Setenv(key, "")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())

	before := runGitTestCommand(t, root, "status", "--porcelain=v1", "--branch")
	_, err := applyGitIdentityPreflight(context.Background(), root)
	require.Error(t, err)
	after := runGitTestCommand(t, root, "status", "--porcelain=v1", "--branch")
	assert.Equal(t, before, after)
}

func TestGitIdentityV2EnvironmentIsScoped(t *testing.T) {
	for _, key := range gitIdentityEnvironmentKeys() {
		t.Setenv(key, "original-"+key)
	}
	identity := gitIdentity{
		AuthorName:     "Author",
		AuthorEmail:    "author@example.com",
		CommitterName:  "Committer",
		CommitterEmail: "committer@example.com",
	}

	var output string
	err := stepv2.PipelineCtx(identity.apply(context.Background()), "identity", func(ctx context.Context) {
		output = stepv2.Cmd(ctx, "sh", "-c", `printf "%s|%s|%s|%s" "$GIT_AUTHOR_NAME" "$GIT_AUTHOR_EMAIL" "$GIT_COMMITTER_NAME" "$GIT_COMMITTER_EMAIL"`)
	})
	require.NoError(t, err)
	assert.Equal(t, "Author|author@example.com|Committer|committer@example.com", output)

	for _, key := range gitIdentityEnvironmentKeys() {
		assert.Equal(t, "original-"+key, os.Getenv(key))
	}
}

func TestGitIdentityPreservesGitAmAuthor(t *testing.T) {
	sourceRepo := filepath.Join(t.TempDir(), "source")
	runGitTestCommand(t, sourceRepo, "init")
	require.NoError(t, os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("base\n"), 0o600))
	runGitTestCommand(t, sourceRepo, "add", "README.md")
	runGitTestCommand(t, sourceRepo, "-c", "user.name=Base Author", "-c", "user.email=base@example.com", "commit", "-m", "base")
	require.NoError(t, os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("patched\n"), 0o600))
	runGitTestCommand(t, sourceRepo, "add", "README.md")
	runGitTestCommand(t, sourceRepo, "-c", "user.name=Patch Author", "-c", "user.email=patch@example.com", "commit", "-m", "patch")
	patch := runGitTestCommand(t, sourceRepo, "format-patch", "-1", "--stdout", "HEAD")

	targetRepo := filepath.Join(t.TempDir(), "target")
	runGitTestCommand(t, targetRepo, "init")
	require.NoError(t, os.WriteFile(filepath.Join(targetRepo, "README.md"), []byte("base\n"), 0o600))
	runGitTestCommand(t, targetRepo, "add", "README.md")
	runGitTestCommand(t, targetRepo, "-c", "user.name=Base Author", "-c", "user.email=base@example.com", "commit", "-m", "base")
	patchPath := filepath.Join(targetRepo, "patch.patch")
	require.NoError(t, os.WriteFile(patchPath, []byte(patch), 0o600))

	identity := gitIdentity{
		AuthorName:     "Resolved User",
		AuthorEmail:    "resolved@example.com",
		CommitterName:  "Resolved User",
		CommitterEmail: "resolved@example.com",
	}
	ok := legacystep.Run(identity.apply(context.Background()),
		legacystep.Cmd("git", "am", patchPath).In(&targetRepo))
	require.True(t, ok)

	output := runGitTestCommand(t, targetRepo, "show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD")
	assert.Equal(t, "Patch Author|patch@example.com|Resolved User|resolved@example.com", strings.TrimSpace(output))
}

func TestRepositoryIdentityPropagatesThroughUpstreamScript(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	sourceRepo := filepath.Join(t.TempDir(), "source")
	runGitTestCommand(t, sourceRepo, "init")
	require.NoError(t, os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("source\n"), 0o600))
	runGitTestCommand(t, sourceRepo, "add", "README.md")
	runGitTestCommand(t, sourceRepo, "-c", "user.name=Source Author", "-c", "user.email=source@example.com", "commit", "-m", "initial")

	runGitTestCommand(t, root, "init")
	runGitTestCommand(t, root, "config", "user.name", "Repository User")
	runGitTestCommand(t, root, "config", "user.email", "repository@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("root\n"), 0o600))
	runGitTestCommand(t, root, "add", "README.md")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	runGitTestCommand(t, root, "-c", "protocol.file.allow=always", "submodule", "add", sourceRepo, "upstream")

	for _, key := range gitIdentityEnvironmentKeys() {
		t.Setenv(key, "")
	}

	scriptDir := filepath.Join(root, "scripts")
	require.NoError(t, os.Mkdir(scriptDir, 0o700))
	scriptPath := filepath.Join(scriptDir, "upstream.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nset -eu\ncd upstream\ngit commit --allow-empty -m identity-test\n"), 0o700))

	identity, err := resolveGitIdentity(context.Background(), root, systemGitIdentitySource{})
	require.NoError(t, err)

	ctx := identity.apply(context.Background())
	ok := legacystep.Run(ctx, legacystep.Cmd("./scripts/upstream.sh").In(&root))
	require.True(t, ok)

	output := runGitTestCommand(t, filepath.Join(root, "upstream"), "show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD")
	assert.Equal(t, "Repository User|repository@example.com|Repository User|repository@example.com", strings.TrimSpace(output))
	for _, key := range gitIdentityEnvironmentKeys() {
		value, ok := os.LookupEnv(key)
		assert.True(t, ok)
		assert.Empty(t, value)
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if args[0] == "init" {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed:\n%s", strings.Join(args, " "), output)
	return string(output)
}
