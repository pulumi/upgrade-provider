package upgrade

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

type contextKeyType struct{}

var contextKey contextKeyType

func GetContext(ctx context.Context) *Context {
	return ctx.Value(contextKey).(*Context)
}

type Context struct {
	// The user's GOPATH env var
	GoPath string
	// An optional path to clone the provider repo to
	repoPath string

	TargetVersion *semver.Version
	InferVersion  bool
	// For CI - check and see if upstream is ahead of this provider.
	// If so, create a GH issue and exit. Do not attempt to upgrade the provider.
	OnlyCheckUpstream bool

	UpgradeBridgeVersion bool
	TargetBridgeRef      Ref

	UpgradePfVersion bool

	UpgradeProviderVersion bool
	MajorVersionBump       bool

	UpgradeJavaVersion bool

	// The unqualified name of the upstream provider.
	//
	// As an example, Pulumi's AWS provider has:
	//
	//	pulumi-aws
	//
	UpstreamProviderName string

	// The desired version of pulumi/{pkg,sdk} to link to.
	//
	// If TargetPulumiVersion is nil, then pulumi/{pkg,sdk} should follow the bridge.
	//
	// Otherwise, we will `replace` with TargetPulumiVersion for both pkg and sdk.
	TargetPulumiVersion Ref

	// The desired java version.
	JavaVersion string
	// The old java version we found.
	oldJavaVersion string

	UpgradeCodeMigration bool
	MigrationOpts        []string

	AllowMissingDocs bool
	RemovePlugins    bool
	PrReviewers      string
	PrAssign         string

	PRDescription string
}

func (c *Context) Wrap(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey, c)
}

func (c *Context) SetRepoPath(p string) {
	c.repoPath = p
}

type HandledError struct{}

var ErrHandled = HandledError{}

func (HandledError) Error() string {
	return "Program failed and displayed the error to the user"
}

type ProviderRepo struct {
	// The path to the repository root
	root string
	// The default git branch of the repository
	defaultBranch string
	// The working branch of the repository
	workingBranch string
	// If there is already a PR on GitHub who is merging from `repo.workingBranch`.
	prAlreadyExists bool

	// The highest version tag released on the repo
	currentVersion *semver.Version

	// The upstream version we are upgrading from.  Because not all upstream providers
	// are go module compliment, we might not be able to always resolve this version.
	currentUpstreamVersion *semver.Version

	name string
	org  string
}

func (p ProviderRepo) providerDir() *string {
	dir := filepath.Join(p.root, "provider")
	return &dir
}

func (p ProviderRepo) examplesDir() *string {
	dir := filepath.Join(p.root, "examples")
	return &dir
}

func (p ProviderRepo) sdkDir() *string {
	dir := filepath.Join(p.root, "sdk")
	return &dir
}

type GoMod struct {
	Kind     RepoKind
	Upstream module.Version
	Fork     *modfile.Replace
	Bridge   module.Version
	Pf       module.Version

	UpstreamProviderOrg string
}

type UpstreamUpgradeTarget struct {
	// The version we are targeting. `nil` indicates that no upstream upgrade was found.
	Version *semver.Version
	// The list of issues that this upgrade will close.
	GHIssues []UpgradeTargetIssue
}

type UpgradeTargetIssue struct {
	Version *semver.Version `json:"-"`
	Number  int             `json:"number"`
}

// Sort git tags by semver.
//
// Tags that don't parse as semver are considered to be less then any tag that does parse.
func latestSemverTag(prefix string, refs gitRepoRefs) *semver.Version {
	trim := func(branch string) string {
		p := "refs/" + refs.kind + "/" + prefix
		if strings.HasPrefix(branch, p) {
			return strings.TrimPrefix(branch, p)
		}
		return ""
	}
	parse := func(branch string) *semver.Version {
		version := trim(branch)
		v, err := semver.NewVersion(version)
		if err != nil {
			return nil
		}
		return v
	}
	sorted := refs.sortedLabels(func(a, b string) bool {
		vA, vB := parse(a), parse(b)
		switch {
		case vA == nil:
			return false
		case vB == nil:
			return true
		default:
			return vA.GreaterThan(vB)
		}
	})
	if len(sorted) == 0 {
		return nil
	}
	v, _ := semver.NewVersion(trim(sorted[0]))
	return v
}
