package upgrade

import (
	"context"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

type Context struct {
	context.Context

	GoPath string

	MaxVersion *semver.Version

	UpgradeBridgeVersion bool

	UpgradeProviderVersion bool
	MajorVersionBump       bool
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

	// The highest version tag released on the repo
	currentVersion *semver.Version

	// The upstream version we are upgrading from.  Because not all upstream providers
	// are go module compliment, we might not be able to always resolve this version.
	currentUpstreamVersion *semver.Version
}

func (p ProviderRepo) providerDir() *string {
	dir := filepath.Join(p.root, "provider")
	return &dir
}

type GoMod struct {
	Kind     RepoKind
	Upstream module.Version
	Fork     *modfile.Replace
	Bridge   module.Version
}

type UpgradeTargetIssue struct {
	Version *semver.Version `json:"-"`
	Number  int             `json:"number"`
}

// The sorted list of upstream versions that will be fixed with this update.
type UpstreamVersions []UpgradeTargetIssue

func (p UpstreamVersions) Latest() *semver.Version {
	return p[0].Version
}
