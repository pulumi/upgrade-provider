package upgrade

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/mod/module"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
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

	UpgradeProviderVersion bool
	MajorVersionBump       bool

	UpgradeJavaVersion     bool
	UpgradeJavaVersionOnly bool

	// Some providers go for months without an upstream release, but do receive weekly bridge updates.
	// upgrade-provider will detect if the provider's last release is more than eight weeks old, and if it is,
	// setting this field to True will trigger a patch release on a non-upstream upgrade.
	MaintenancePatch bool

	// The unqualified name of the upstream provider.
	//
	// As an example, Pulumi's AWS provider has:
	//
	//	pulumi-aws
	//
	UpstreamProviderName string
	// The org component in the upstream provider's repo path.
	//
	// This must be provided when upstream is hosted at a repo that does
	// not match the Go Module path it is declared as. Otherwise it can
	// be inferred.
	//
	// For example, if we have an upstream provider with import path:
	//
	//	github.com/terraform-providers/terraform-provider-my-provider
	//
	// which is hosted at:
	//
	//	github.com/my-org/terraform-provider-my-provider
	//
	// Then UpstreamProviderOrg should be `my-org`.
	UpstreamProviderOrg string

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

	AllowMissingDocs bool
	PrReviewers      string
	PrAssign         string

	PRDescription string
	PRTitlePrefix string
}

// Check if the user specified operating in the current working directory (CWD) with `--repo-path=.`. In this case the
// tool can assume all Git fetching and sumbodule resolution has been done already by the user.
func (c *Context) IsCWD() bool {
	if c.repoPath == "" {
		return false
	}
	cwd, err := filepath.Abs(".")
	contract.AssertNoErrorf(err, "filepath.Abs failed unexpectedly")
	rp, err := filepath.Abs(c.repoPath)
	contract.AssertNoErrorf(err, "filepath.Abs failed unexpectedly")
	return cwd == rp
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
	// The title of the PR to be created
	prTitle string
	// If there is already a PR on GitHub who is merging from `repo.workingBranch`.
	prAlreadyExists bool

	// The highest version tag released on the repo
	currentVersion *semver.Version

	// The upstream version we are upgrading from.  Because not all upstream providers
	// are go module compliment, we might not be able to always resolve this version.
	currentUpstreamVersion *semver.Version

	Name string
	Org  string
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
	Bridge   module.Version
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
