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

	TargetVersion *semver.Version
	InferVersion  bool

	UpgradeBridgeVersion bool
	UpgradeSdkVersion    bool

	UpgradeProviderVersion bool
	MajorVersionBump       bool

	UpstreamProviderName string

	UpgradeCodeMigration bool
	MigrationOpts        []string
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

type GoMod struct {
	Kind     RepoKind
	Upstream module.Version
	Fork     *modfile.Replace
	Bridge   module.Version

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
