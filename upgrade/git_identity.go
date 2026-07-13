package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	legacystep "github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

const (
	gitAuthorName     = "GIT_AUTHOR_NAME"
	gitAuthorEmail    = "GIT_AUTHOR_EMAIL"
	gitCommitterName  = "GIT_COMMITTER_NAME"
	gitCommitterEmail = "GIT_COMMITTER_EMAIL"
)

// gitIdentityEnvironmentKeys returns the standard Git environment variables
// required to provide complete author and committer identities.
func gitIdentityEnvironmentKeys() []string {
	return []string{
		gitAuthorName,
		gitAuthorEmail,
		gitCommitterName,
		gitCommitterEmail,
	}
}

// gitIdentity is the complete identity exported to child Git processes.
// Author and committer values are tracked separately so explicit caller values
// can be preserved while missing fields are filled from later sources.
type gitIdentity struct {
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
}

// complete reports whether Git can create commits without consulting config.
func (i gitIdentity) complete() bool {
	return i.AuthorName != "" && i.AuthorEmail != "" &&
		i.CommitterName != "" && i.CommitterEmail != ""
}

// environment converts the identity to Git's standard process environment.
func (i gitIdentity) environment() map[string]string {
	return map[string]string{
		gitAuthorName:     i.AuthorName,
		gitAuthorEmail:    i.AuthorEmail,
		gitCommitterName:  i.CommitterName,
		gitCommitterEmail: i.CommitterEmail,
	}
}

// apply scopes the identity to both command implementations used by the
// upgrade pipeline. Legacy steps receive an explicit exec.Cmd environment;
// step/v2 enters and restores its existing scoped environment around commands.
// Git am still takes authorship from each patch and uses this identity only as
// the committer.
func (i gitIdentity) apply(ctx context.Context) context.Context {
	environment := i.environment()
	ctx = legacystep.WithCommandEnv(ctx, environment)
	for _, key := range gitIdentityEnvironmentKeys() {
		ctx = stepv2.WithEnv(ctx, &stepv2.EnvVar{Key: key, Value: environment[key]})
	}
	return ctx
}

// gitIdentitySource isolates environment and Git config lookups so precedence
// and failure behavior can be tested without changing process state.
type gitIdentitySource interface {
	LookupEnv(string) (string, bool)
	GitConfig(context.Context, string, string) (string, error)
}

// systemGitIdentitySource reads identity sources from the current process and
// provider repository.
type systemGitIdentitySource struct{}

func (systemGitIdentitySource) LookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

func (systemGitIdentitySource) GitConfig(ctx context.Context, repoRoot, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	return "", fmt.Errorf("read effective Git configuration %q in %q: %w", key, repoRoot, err)
}

// fillMissingIdentity applies one name/email source without replacing fields
// supplied by a higher-precedence source.
func fillMissingIdentity(identity *gitIdentity, name, email string) {
	if identity.AuthorName == "" {
		identity.AuthorName = name
	}
	if identity.AuthorEmail == "" {
		identity.AuthorEmail = email
	}
	if identity.CommitterName == "" {
		identity.CommitterName = name
	}
	if identity.CommitterEmail == "" {
		identity.CommitterEmail = email
	}
}

// resolveGitIdentity resolves each identity field in precedence order from
// explicit Git environment and effective provider-repository config. It
// returns an actionable error unless all four author and committer fields can
// be resolved.
func resolveGitIdentity(ctx context.Context, repoRoot string, source gitIdentitySource) (gitIdentity, error) {
	identity := gitIdentity{}
	explicit := map[string]*string{
		gitAuthorName:     &identity.AuthorName,
		gitAuthorEmail:    &identity.AuthorEmail,
		gitCommitterName:  &identity.CommitterName,
		gitCommitterEmail: &identity.CommitterEmail,
	}
	for _, key := range gitIdentityEnvironmentKeys() {
		if value, ok := source.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
			*explicit[key] = value
		}
	}
	if identity.complete() {
		return identity, nil
	}

	name, err := source.GitConfig(ctx, repoRoot, "user.name")
	if err != nil {
		return gitIdentity{}, err
	}
	email, err := source.GitConfig(ctx, repoRoot, "user.email")
	if err != nil {
		return gitIdentity{}, err
	}
	fillMissingIdentity(&identity, strings.TrimSpace(name), strings.TrimSpace(email))
	if identity.complete() {
		return identity, nil
	}

	message := fmt.Sprintf(`Git author and committer identity is required to run the patched-provider workflow in %q.
Supply non-empty %s, %s, %s, and %s environment variables, or configure the provider repository:

  git -C %s config user.name "Your Name"
  git -C %s config user.email "you@example.com"

Global Git configuration is not required.`, repoRoot,
		gitAuthorName, gitAuthorEmail, gitCommitterName, gitCommitterEmail,
		strconv.Quote(filepath.Clean(repoRoot)), strconv.Quote(filepath.Clean(repoRoot)))
	return gitIdentity{}, errors.New(message)
}

// resolveGitIdentityStep exposes resolution through the replay-aware step/v2
// machinery. Identity lookup is impure because it reads environment and Git
// configuration.
func resolveGitIdentityStep(ctx context.Context, repoRoot string) gitIdentity {
	resolve := stepv2.Func11E("Resolve Git Identity", func(
		ctx context.Context, repoRoot string,
	) (gitIdentity, error) {
		stepv2.MarkImpure(ctx)
		return resolveGitIdentity(ctx, repoRoot, systemGitIdentitySource{})
	})
	return resolve(ctx, repoRoot)
}

// applyGitIdentityPreflight resolves identity and returns a context that passes
// it to the patched-provider workflow. Callers must invoke this immediately
// before running scripts/upstream.sh.
func applyGitIdentityPreflight(ctx context.Context, repoRoot string) (context.Context, error) {
	var identity gitIdentity
	err := stepv2.PipelineCtx(ctx, "Git Identity Preflight", func(ctx context.Context) {
		identity = resolveGitIdentityStep(ctx, repoRoot)
	})
	if err != nil {
		return ctx, err
	}
	return identity.apply(ctx), nil
}
