package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/spf13/cobra"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
)

type Context struct {
	context.Context

	GoPath string

	MaxVersion *semver.Version

	UpgradeBridgeVersion bool

	UpgradeProviderVersion bool
	MajorVersionBump       bool
}

func cmd() *cobra.Command {
	var maxVersion string
	gopath, ok := os.LookupEnv("GOPATH")
	if !ok {
		gopath = build.Default.GOPATH
	}
	var upgradeKind string

	context := Context{
		Context: context.Background(),
		GoPath:  gopath,
	}

	exitOnError := func(err error) {
		if err == nil {
			return
		}
		if !errors.Is(err, ErrHandled) {
			fmt.Printf("error: %s\n", err.Error())
		}
		os.Exit(1)
	}

	cmd := &cobra.Command{
		Use:   "upgrade-provider",
		Short: "upgrade-provider automatics the process of upgrading a TF-bridged provider",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			// Validate that maxVersion is a valid version
			var err error
			if maxVersion != "" {
				context.MaxVersion, err = semver.NewVersion(maxVersion)
				if err != nil {
					return fmt.Errorf("--provider-version=%s: %w",
						maxVersion, err)
				}
			}

			// Validate the kind switch
			switch upgradeKind {
			case "all":
				context.UpgradeBridgeVersion = true
				context.UpgradeProviderVersion = true
			case "bridge":
				context.UpgradeBridgeVersion = true
			case "provider":
				context.UpgradeProviderVersion = true
			default:
				return fmt.Errorf(
					"--kind=%s invalid. Must be one of `all`, `bridge` or `provider`.",
					upgradeKind)
			}

			if context.MaxVersion != nil && !context.UpgradeProviderVersion {
				return fmt.Errorf(
					"cannot specify the provider version unless the provider will be upgraded")
			}

			return nil
		},
		Run: func(_ *cobra.Command, args []string) {
			err := UpgradeProvider(context, args[0])
			exitOnError(err)
		},
	}

	cmd.PersistentFlags().StringVar(&maxVersion, "provider-version", "",
		`Upgrade the provider to the passed in version.

If the passed version does not exist, an error is signaled.`)

	cmd.PersistentFlags().BoolVar(&context.MajorVersionBump, "major", false,
		`Upgrade the provider to a new major version.`)

	cmd.PersistentFlags().StringVar(&upgradeKind, "kind", "all",
		`The kind of upgrade to perform:
- "all":     Upgrade the upstream provider and the bridge.
- "bridge":  Upgrade the bridge only.
- "provider: Upgrade the upstream provider only.`)

	return cmd
}

func main() {
	err := cmd().Execute()
	contract.IgnoreError(err)
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

// The sorted list of upstream versions that will be fixed with this update.
type UpstreamVersions []UpgradeTargetIssue

func (p UpstreamVersions) Latest() *semver.Version {
	return p[0].Version
}

func UpgradeProvider(ctx Context, name string) error {
	var err error
	var repo ProviderRepo
	var targetBridgeVersion string
	var upgradeTargets UpstreamVersions
	var goMod *GoMod
	upstreamProviderName := strings.TrimPrefix(name, "pulumi-")
	if s, ok := ProviderName[upstreamProviderName]; ok {
		upstreamProviderName = s
	}

	ok := step.Run(step.Combined("Setting Up Environment",
		step.Env("GOWORK", "off"),
		step.Env("PULUMI_MISSING_DOCS_ERROR", "true"),
	))
	if !ok {
		return ErrHandled
	}

	discoverSteps := []step.Step{
		pulumiProviderRepos(ctx, name).AssignTo(&repo.root),
		pullDefaultBranch(ctx, "origin").In(&repo.root).
			AssignTo(&repo.defaultBranch),
	}

	discoverSteps = append(discoverSteps, step.F("Repo kind", func() (string, error) {
		goMod, err = repoKind(ctx, repo, upstreamProviderName)
		if err != nil {
			return "", err
		}
		return string(goMod.Kind), nil
	}))

	if ctx.UpgradeProviderVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Provider Update", func() (string, error) {
				var msg string
				upgradeTargets, msg, err = getExpectedTarget(ctx, name)
				if err != nil {
					return "", err
				}

				// If we have upgrades to perform, we list the new version we will target
				if len(upgradeTargets) == 0 {
					// Otherwise, we don't bother to try to upgrade the provider.
					ctx.UpgradeProviderVersion = false
					ctx.MajorVersionBump = false
					return "Up to date" + msg, nil
				}

				switch {
				case goMod.Kind.IsPatched():
					err = setCurrentUpstreamFromPatched(ctx, &repo)
				case goMod.Kind.IsForked():
					err = setCurrentUpstreamFromForked(ctx, &repo, goMod)
				case goMod.Kind.IsShimmed():
					err = setCurrentUpstreamFromShimmed(ctx, &repo, goMod)
				case goMod.Kind == Plain:
					err = setCurrentUpstreamFromPlain(ctx, &repo, goMod)
				default:
					return "", fmt.Errorf("Unexpected repo kind: %s", goMod.Kind)
				}
				if err != nil {
					return "", fmt.Errorf("current upstream version: %w", err)
				}

				var previous string
				if repo.currentUpstreamVersion != nil {
					previous = fmt.Sprintf("%s -> ", repo.currentUpstreamVersion)
				}

				return previous + upgradeTargets.Latest().String() + msg, nil
			}))
	}

	if ctx.UpgradeBridgeVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Bridge Update", func() (string, error) {
				latest, err := latestRelease(ctx, "pulumi/pulumi-terraform-bridge")
				if err != nil {
					return "", err
				}

				// If our target upgrade version is the same as our current version, we skip the update.
				if latest.Original() == goMod.Bridge.Version {
					ctx.UpgradeBridgeVersion = false
					return fmt.Sprintf("Up to date at %s", latest.Original()), nil
				}

				targetBridgeVersion = latest.Original()
				return fmt.Sprintf("%s -> %s", goMod.Bridge.Version, latest.Original()), nil
			}))
	}

	if ctx.MajorVersionBump {
		discoverSteps = append(discoverSteps,
			step.F("Current Major Version", func() (string, error) {
				var err error
				repo.currentVersion, err = latestRelease(ctx, "pulumi/"+name)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d", repo.currentVersion.Major()), nil
			}))
	}

	ok = step.Run(step.Combined("Discovering Repository", discoverSteps...))
	if !ok {
		return ErrHandled
	}

	if ctx.UpgradeProviderVersion {
		shouldMajorVersionBump := repo.currentUpstreamVersion.Major() != upgradeTargets.Latest().Major()
		if ctx.MajorVersionBump && !shouldMajorVersionBump {
			return fmt.Errorf("--major version update indicated, but no major upgrade available (already on v%d)",
				repo.currentUpstreamVersion.Major())
		} else if !ctx.MajorVersionBump && shouldMajorVersionBump {
			return fmt.Errorf("This is a major version update (v%d -> v%d), but --major was not passed",
				repo.currentUpstreamVersion.Major(), upgradeTargets.Latest().Major())
		}
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion {
		fmt.Println(colorize.Bold("No actions needed"))
		return nil
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() && ctx.UpgradeProviderVersion {
		ok = step.Run(upgradeUpstreamFork(ctx, name, upgradeTargets.Latest(), goMod).
			AssignTo(&forkedProviderUpstreamCommit))
		if !ok {
			return ErrHandled
		}
	}

	var targetSHA string
	if ctx.UpgradeProviderVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-terraform-provider-%s-to-v%s",
			upstreamProviderName, upgradeTargets.Latest())
	} else if ctx.UpgradeBridgeVersion {
		contract.Assertf(targetBridgeVersion != "",
			"We are upgrading the bridge, so we must have a target version")
		repo.workingBranch = fmt.Sprintf("upgrade-pulumi-terraform-bridge-to-%s",
			targetBridgeVersion)
	} else {
		return fmt.Errorf("calculating branch name: unknown action")
	}
	steps := []step.Step{
		ensureBranchCheckedOut(ctx, repo.workingBranch).In(&repo.root),
	}

	if ctx.MajorVersionBump {
		steps = append(steps, majorVersionBump(ctx, goMod, upgradeTargets, repo))

		defer func() {
			fmt.Printf("\n\n" + colorize.Warn("Major Version Updates are not fully automated!") + "\n")
			fmt.Printf("Steps 1..9, 12 and 13 have been automated. Step 11 can be skipped.\n")
			fmt.Printf("%s need to complete Step 10: Updating README.md and sdk/python/README.md "+
				"in a follow up commit.\n", colorize.Bold("You"))
			fmt.Printf("Steps are listed at\n\t" +
				"https://github.com/pulumi/platform-providers-team/blob/main/playbooks/tf-provider-major-version-update.md\n")
		}()
	}

	if ctx.UpgradeProviderVersion {
		steps = append(steps, upgradeProviderVersion(ctx, goMod, upgradeTargets.Latest(), repo,
			targetSHA, forkedProviderUpstreamCommit))
	} else if goMod.Kind.IsPatched() {
		// If we are upgrading the provider version, then the upgrade will leave
		// `upstream` in a usable state. Otherwise, we need to call `make
		// upstream` to ensure that the module is valid (for `go get` and `go mod
		// tidy`.
		steps = append(steps, step.Cmd(exec.CommandContext(ctx, "make", "upstream")).In(&repo.root))
	}

	if ctx.UpgradeBridgeVersion {
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "get", "github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion)).
			In(repo.providerDir()))
	}

	artifacts := append(steps,
		step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(repo.providerDir()),
		step.Cmd(exec.CommandContext(ctx, "pulumi", "plugin", "rm", "--all", "--yes")),
		step.Cmd(exec.CommandContext(ctx, "make", "tfgen")).In(&repo.root),
		step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
		gitCommit(ctx, "make tfgen").In(&repo.root),
		step.Cmd(exec.CommandContext(ctx, "make", "build_sdks")).In(&repo.root),
		step.Computed(func() step.Step {
			if !ctx.MajorVersionBump {
				return nil
			}

			return updateFile("Update module in sdk/go.mod", "sdk/go.mod", func(b []byte) ([]byte, error) {
				base := "module github.com/pulumi/" + name + "/sdk"
				old := base
				if repo.currentVersion.Major() > 1 {
					old += fmt.Sprintf("/v%d", repo.currentVersion.Major())
				}
				new := base + fmt.Sprintf("/v%d", repo.currentVersion.Major()+1)
				return bytes.ReplaceAll(b, []byte(old), []byte(new)), nil
			}).In(&repo.root)
		}),
		step.Computed(func() step.Step {
			if !ctx.MajorVersionBump {
				return nil
			}
			dir := filepath.Join(repo.root, "sdk")
			return step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).
				In(&dir)
		}),
		step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
		gitCommit(ctx, "make build_sdks").In(&repo.root),
		informGitHub(ctx, upgradeTargets, repo, goMod,
			upstreamProviderName, targetBridgeVersion),
	)

	ok = step.Run(step.Combined("Update Artifacts", artifacts...))
	if !ok {
		return ErrHandled
	}

	return nil
}

// A "git commit" step that is resilient to no changes in the directory.
//
// This is required to accommodate failure and retry in the `git` push steps.
func gitCommit(ctx context.Context, msg string) step.Step {
	return step.Computed(func() step.Step {
		check, err := exec.CommandContext(ctx, "git", "status", "--porcelain=1").CombinedOutput()
		description := fmt.Sprintf(`git commit -m "%s"`, msg)
		if err != nil {
			return step.F("git commit", func() (string, error) {
				return "", err
			})
		}
		if len(check) > 0 {
			return step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", msg))
		}
		return step.F(description, func() (string, error) {
			return "nothing to commit", nil
		})
	})
}

type RepoKind string

const (
	Plain             RepoKind = "plain"
	Forked            RepoKind = "forked"
	Shimmed           RepoKind = "shimmed"
	ForkedAndShimmed  RepoKind = "forked & shimmed"
	Patched           RepoKind = "patched"
	PatchedAndShimmed RepoKind = "patched & shimmed"
)

func (rk RepoKind) Shimmed() RepoKind {
	switch rk {
	case Plain:
		return Shimmed
	case Forked:
		return ForkedAndShimmed
	default:
		return rk
	}
}

func (rk RepoKind) Patched() RepoKind {
	switch rk {
	case Plain:
		return Patched
	case Shimmed:
		return PatchedAndShimmed
	case Forked, ForkedAndShimmed:
		panic("Cannot have a forked and patched provider")
	default:
		return rk
	}
}

func (rk RepoKind) IsForked() bool {
	switch rk {
	case Forked:
		fallthrough
	case ForkedAndShimmed:
		return true
	default:
		return false
	}
}

func (rk RepoKind) IsShimmed() bool {
	switch rk {
	case Shimmed:
		fallthrough
	case ForkedAndShimmed:
		return true
	default:
		return false
	}
}

func (rk RepoKind) IsPatched() bool {
	switch rk {
	case Patched:
		fallthrough
	case PatchedAndShimmed:
		return true
	default:
		return false
	}
}

var versionSuffix = regexp.MustCompile("/v[2-9][0-9]*$")

func ensurePulumiRemote(ctx Context, name string) (string, error) {
	remotes, err := runGitCommand(ctx, func(b []byte) ([]string, error) {
		return strings.Split(string(b), "\n"), nil
	}, "remote")
	if err != nil {
		return "", fmt.Errorf("listing remotes: %w", err)
	}
	for _, remote := range remotes {
		if remote == "pulumi" {
			return "'pulumi' already exists", nil
		}
	}
	return runGitCommand(ctx, func([]byte) (string, error) {
		return "set to 'pulumi'", nil
	}, "remote", "add", "pulumi",
		fmt.Sprintf("https://github.com/pulumi/terraform-provider-%s.git", name))
}

func ensureBranchCheckedOut(ctx Context, branchName string) step.Step {
	var branches string
	var alreadyExists bool
	var alreadyCurrent bool
	return step.Combined("Ensure Branch",
		step.Cmd(exec.CommandContext(ctx, "git", "branch")).AssignTo(&branches),
		step.F("Already exists", func() (string, error) {
			lines := strings.Split(branches, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == branchName {
					alreadyExists = true
					return "yes", nil
				}
				if line == "* "+branchName {
					alreadyCurrent = true
					return "yes, current branch", nil
				}
			}
			return "no", nil
		}),

		step.Computed(func() step.Step {
			if alreadyExists || alreadyCurrent {
				return nil
			}
			return step.Cmd(exec.CommandContext(ctx, "git", "checkout", "-b", branchName))
		}),
		step.Computed(func() step.Step {
			if alreadyCurrent {
				return nil
			}
			return step.Cmd(exec.CommandContext(ctx, "git", "checkout", branchName))
		}),
	)
}

type GoMod struct {
	Kind     RepoKind
	Upstream module.Version
	Fork     *modfile.Replace
	Bridge   module.Version
}

func modPathWithoutVersion(path string) string {
	if match := versionSuffix.FindStringIndex(path); match != nil {
		return path[:match[0]]
	}
	return path
}

// Find the go module version of needleModule, searching from the default repo branch, not
// the currently checked out code.
func originalGoVersionOf(ctx context.Context, repo ProviderRepo, file, needleModule string) (module.Version, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "show", repo.defaultBranch+":"+file)
	cmd.Dir = repo.root
	data, err := cmd.Output()
	if err != nil {
		return module.Version{}, false, fmt.Errorf("%s:%s: %w",
			repo.defaultBranch, file, err)
	}

	goMod, err := modfile.Parse(file, data, nil)
	if err != nil {
		return module.Version{}, false, fmt.Errorf("%s:%s: %w",
			repo.defaultBranch, file, err)
	}

	needleModule = modPathWithoutVersion(needleModule)

	for _, req := range goMod.Replace {
		path := modPathWithoutVersion(req.New.Path)
		if path == needleModule {
			return req.New, true, nil
		}
	}
	for _, req := range goMod.Require {
		path := modPathWithoutVersion(req.Mod.Path)
		if path == needleModule {
			return req.Mod, true, nil
		}
	}
	return module.Version{}, false, nil
}

func repoKind(ctx context.Context, repo ProviderRepo, providerName string) (*GoMod, error) {
	path := repo.root
	file := filepath.Join(path, "provider", "go.mod")

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}

	goMod, err := modfile.Parse(file, data, nil)
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}

	bridge, ok, err := originalGoVersionOf(ctx, repo, filepath.Join("provider", "go.mod"), "github.com/pulumi/pulumi-terraform-bridge")
	bridgeMissingMsg := "Unable to discover pulumi-terraform-bridge version"
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bridgeMissingMsg, err)
	} else if !ok {
		return nil, fmt.Errorf(bridgeMissingMsg)
	}

	tfProviderRepoName := getTfProviderRepoName(providerName)

	getUpstream := func(file *modfile.File) (*modfile.Require, error) {
		// Find the name of our upstream dependency
		for _, mod := range file.Require {
			pathWithoutVersion := modPathWithoutVersion(mod.Mod.Path)
			if strings.HasSuffix(pathWithoutVersion, tfProviderRepoName) {
				return mod, nil
			}
		}
		return nil, fmt.Errorf("could not find upstream '%s' in go.mod", tfProviderRepoName)
	}

	var upstream *modfile.Require
	var patched bool
	patchDir := filepath.Join(path, "upstream")
	if _, err := os.Stat(patchDir); err == nil {
		patched = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	shimDir := filepath.Join(path, "provider", "shim")
	_, err = os.Stat(shimDir)
	var shimmed bool
	if err == nil {
		shimmed = true
		modPath := filepath.Join(shimDir, "go.mod")
		data, err := os.ReadFile(modPath)
		if err != nil {
			return nil, err
		}
		shimMod, err := modfile.Parse(modPath, data, nil)
		if err != nil {
			return nil, fmt.Errorf("shim/go.mod: %w", err)
		}
		upstream, err = getUpstream(shimMod)
		if err != nil {
			return nil, fmt.Errorf("shim/go.mod: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("unexpected error reading '%s': %w", shimDir, err)
	} else {
		upstream, err = getUpstream(goMod)
		if err != nil {
			return nil, fmt.Errorf("go.mod: %w", err)
		}
	}

	contract.Assertf(upstream != nil, "upstream cannot be nil")

	// If we find a replace that points to a pulumi hosted repo, that indicates a fork.
	var fork *modfile.Replace
	for _, replace := range goMod.Replace {
		// If we're not replacing our upstream, we don't care here
		if replace.Old.Path != upstream.Mod.Path {
			continue
		}
		before, after, found := strings.Cut(replace.New.Path, "/"+tfProviderRepoName)
		if !found || (after != "" && !versionSuffix.MatchString(after)) {
			if replace.New.Path == "../upstream" {
				// We have found a patched provider, so we can just exit here.
				break
			}
			return nil, fmt.Errorf("go.mod: replace has incorrect repo: '%s'", replace.New.Path)
		}
		repoOrgSeperator := strings.LastIndexByte(before, '/')
		org := before[repoOrgSeperator+1:]
		if org != "pulumi" {
			return nil, fmt.Errorf("go.mod: tf fork maintained by '%s': expected 'pulumi'", org)
		}
		fork = replace
		break
	}

	out := GoMod{
		Upstream: upstream.Mod,
		Fork:     fork,
		Bridge:   bridge,
	}

	if fork == nil {
		out.Kind = Plain
	} else {
		out.Kind = Forked
	}

	if shimmed {
		out.Kind = out.Kind.Shimmed()
	}
	if patched {
		out.Kind = out.Kind.Patched()
	}

	return &out, nil
}

type UpgradeTargetIssue struct {
	Version *semver.Version `json:"-"`
	Number  int             `json:"number"`
}

// Fetch the expected upgrade target from github. Return a list of open upgrade issues,
// sorted by semantic version. The list may be empty.
//
// The second argument represents a message to describe the result. It may be empty.
func getExpectedTarget(ctx Context, name string) ([]UpgradeTargetIssue, string, error) {
	getIssues := exec.CommandContext(ctx, "gh", "issue", "list",
		"--state=open",
		"--author=pulumi-bot",
		"--repo=pulumi/"+name,
		"--limit=100",
		"--json=title,number")
	bytes := new(bytes.Buffer)
	getIssues.Stdout = bytes
	err := getIssues.Run()
	if err != nil {
		return nil, "", err
	}
	titles := []struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	}{}
	err = json.Unmarshal(bytes.Bytes(), &titles)
	if err != nil {
		return nil, "", err
	}

	var versions []UpgradeTargetIssue
	var versionConstrained bool
	for _, title := range titles {
		_, nameToVersion, found := strings.Cut(title.Title, "Upgrade terraform-provider-")
		if !found {
			continue
		}
		_, version, found := strings.Cut(nameToVersion, " to ")
		if !found {
			continue
		}
		v, err := semver.NewVersion(version)
		if err == nil {
			if !(ctx.MaxVersion == nil || ctx.MaxVersion.Equal(v) || ctx.MaxVersion.GreaterThan(v)) {
				versionConstrained = true
				continue
			}
			versions = append(versions, UpgradeTargetIssue{
				Version: v,
				Number:  title.Number,
			})
		}
	}
	if len(versions) == 0 {
		var extra string
		if versionConstrained {
			extra = " (a version was found but it was greater then the specified max)"
		}
		return nil, extra, nil
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[j].Version.LessThan(versions[i].Version)
	})

	if ctx.MaxVersion != nil && !versions[0].Version.Equal(ctx.MaxVersion) {
		return nil, "", fmt.Errorf("possible upgrades exist, but non match %s", ctx.MaxVersion)
	}

	return versions, "", nil
}

func pullDefaultBranch(ctx Context, remote string) step.Step {
	var lsRemoteHeads string
	var defaultBranch string
	return step.Combined("pull default branch",
		step.Cmd(exec.Command("git", "ls-remote", "--heads", remote)).AssignTo(&lsRemoteHeads),
		step.F("finding default branch", func() (string, error) {
			var hasMaster bool
			lines := strings.Split(lsRemoteHeads, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				_, ref, found := strings.Cut(line, "\t")
				contract.Assert(found)
				branch := strings.TrimPrefix(ref, "refs/heads/")
				if branch == "master" {
					hasMaster = true
				}
				if branch == "main" {
					return branch, nil
				}
			}
			if hasMaster {
				return "master", nil
			}
			return "", fmt.Errorf("could not find 'master' or 'main' branch")
		}).AssignTo(&defaultBranch),
		step.Computed(func() step.Step {
			return step.Cmd(exec.CommandContext(ctx, "git", "checkout", defaultBranch))
		}),
		step.Cmd(exec.CommandContext(ctx, "git", "pull", remote)),
	).Return(&defaultBranch)
}

func runGitCommand[T any](
	ctx context.Context, filter func([]byte) (T, error), args ...string,
) (result T, err error) {
	var t T

	cmd := exec.CommandContext(ctx, "git", args...)
	if filter != nil {
		out := new(bytes.Buffer)
		cmd.Stdout = out
		err = cmd.Run()
		if err != nil {
			return t, err
		}
		return filter(out.Bytes())
	}
	return t, cmd.Run()
}

func say(msg string) func([]byte) (string, error) {
	return func([]byte) (string, error) {
		return msg, nil
	}
}
func downloadRepo(ctx Context, url, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", url, dst)
	return cmd.Run()
}

func pulumiProviderRepos(ctx Context, name string) step.Step {
	return ensureUpstreamRepo(ctx, path.Join("github.com", "pulumi", name))
}

func ensureUpstreamRepo(ctx Context, repoPath string) step.Step {
	var expectedLocation string
	var repoExists bool
	return step.Combined("Ensure '"+repoPath+"'",
		step.F("Expected Location", func() (string, error) {
			// Strip version
			if match := versionSuffix.FindStringIndex(repoPath); match != nil {
				repoPath = repoPath[:match[0]]
			}

			if prefix, repo, found := strings.Cut(repoPath, "/terraform-providers/"); found {
				name := strings.TrimPrefix(repo, "terraform-provider-")
				org, ok := ProviderOrgs[name]
				if !ok {
					return "", fmt.Errorf("terraform-providers based path: missing remap for '%s'", name)
				}
				repoPath = prefix + "/" + org + "/" + repo
			}

			// from github.com/org/repo to $GOPATH/src/github.com/org
			expectedLocation = filepath.Join(strings.Split(repoPath, "/")...)
			expectedLocation = filepath.Join(ctx.GoPath, "src", expectedLocation)
			if info, err := os.Stat(expectedLocation); err == nil {
				if !info.IsDir() {
					return "", fmt.Errorf("'%s' not a directory", expectedLocation)
				}
				repoExists = true
			}
			return expectedLocation, nil
		}),
		step.F("Downloading", func() (string, error) {
			if repoExists {
				return "skipped", nil
			}
			targetDir := filepath.Dir(expectedLocation)
			err := os.MkdirAll(targetDir, 0700)
			if err != nil && !os.IsExist(err) {
				return "", err
			}

			targetURL := fmt.Sprintf("https://%s.git", repoPath)
			return "done", downloadRepo(ctx, targetURL, expectedLocation)
		}),
		step.F("Validating", func() (string, error) {
			return "done", exec.CommandContext(ctx, "git", "status", "--short").Run()
		}).In(&expectedLocation),
	).Return(&expectedLocation)
}

func prBody(ctx Context, repo ProviderRepo, upgradeTargets UpstreamVersions, goMod *GoMod, targetBridge, upstreamProviderName string) string {
	b := new(strings.Builder)
	fmt.Fprintf(b, "This PR was generated via `$ upgrade-provider %s`.\n",
		strings.Join(os.Args[1:], " "))

	fmt.Fprintf(b, "\n---\n\n")

	if ctx.MajorVersionBump {
		fmt.Fprintf(b, "Updating major version from %s to %s.\n", repo.currentVersion, repo.currentVersion.IncMajor())
	}

	if ctx.UpgradeProviderVersion {
		var prev string
		if repo.currentUpstreamVersion != nil {
			prev = fmt.Sprintf("from %s ", repo.currentUpstreamVersion)
		}
		fmt.Fprintf(b, "Upgrading terraform-provider-%s %sto %s.\n",
			upstreamProviderName, prev, upgradeTargets.Latest())
	}
	if ctx.UpgradeBridgeVersion {
		fmt.Fprintf(b, "Upgrading pulumi-terraform-bridge from %s to %s.\n",
			goMod.Bridge.Version, targetBridge)
	}

	if len(upgradeTargets) > 0 {
		fmt.Fprintf(b, "\n")
	}
	for _, t := range upgradeTargets {
		fmt.Fprintf(b, "Fixes #%d\n", t.Number)
	}
	return b.String()
}

func getTfProviderRepoName(providerName string) string {
	if tfRepoName, ok := ProviderName[providerName]; ok {
		providerName = tfRepoName
	}
	return "terraform-provider-" + providerName
}

// Upgrade the upstream fork of a pulumi provider.
//
// The SHA of the new upstream branch is returned.
func upgradeUpstreamFork(ctx Context, name string, target *semver.Version, goMod *GoMod) step.Step {
	var forkedProviderUpstreamCommit string
	var upstreamPath string
	var previousUpstreamVersion *semver.Version
	return step.Combined("Upgrading Forked Provider",
		ensureUpstreamRepo(ctx, goMod.Fork.Old.Path).AssignTo(&upstreamPath),
		step.F("Ensure Pulumi Remote", func() (string, error) {
			remoteName := strings.TrimPrefix(name, "pulumi-")
			if s, ok := ProviderName[remoteName]; ok {
				remoteName = s
			}
			return ensurePulumiRemote(ctx, remoteName)
		}).In(&upstreamPath),
		step.Cmd(exec.Command("git", "fetch", "pulumi")).In(&upstreamPath),
		step.Cmd(exec.Command("git", "fetch", "origin", "--tags")).In(&upstreamPath),
		step.F("Discover Previous Upstream Version", func() (string, error) {
			return runGitCommand(ctx, func(b []byte) (string, error) {
				lines := strings.Split(string(b), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					version, err := semver.NewVersion(strings.TrimPrefix(line, "pulumi/upstream-v"))
					if err != nil {
						continue
					}
					if previousUpstreamVersion == nil || previousUpstreamVersion.LessThan(version) {
						previousUpstreamVersion = version
					}
				}
				if previousUpstreamVersion == nil {
					return "", fmt.Errorf("no version found")
				}
				return previousUpstreamVersion.String(), nil
			}, "branch", "--remote", "--list", "pulumi/upstream-v*")
		}).In(&upstreamPath),
		step.Computed(func() step.Step {
			return step.Cmd(exec.CommandContext(ctx,
				"git", "checkout", "pulumi/upstream-v"+previousUpstreamVersion.String()))
		}).In(&upstreamPath),
		step.F("Upstream Branch", func() (string, error) {
			target := "upstream-v" + target.String()
			branchExists, err := runGitCommand(ctx, func(b []byte) (bool, error) {
				lines := strings.Split(string(b), "\n")
				for _, line := range lines {
					if strings.TrimSpace(line) == target {
						return true, nil
					}
				}
				return false, nil
			}, "branch")
			if err != nil {
				return "", err
			}
			if !branchExists {
				return runGitCommand(ctx, say("creating "+target),
					"checkout", "-b", target)
			}
			return target + " already exists", nil
		}).In(&upstreamPath),
		step.Cmd(exec.CommandContext(ctx,
			"git", "merge", "v"+target.String())).In(&upstreamPath),
		step.Cmd(exec.CommandContext(ctx, "go", "build", ".")).In(&upstreamPath),
		step.Cmd(exec.CommandContext(ctx,
			"git", "push", "pulumi", "upstream-v"+target.String())).In(&upstreamPath),
		step.F("Get Head Commit", func() (string, error) {
			return runGitCommand(ctx, func(b []byte) (string, error) {
				return strings.TrimSpace(string(b)), nil
			}, "rev-parse", "HEAD")
		}).AssignTo(&forkedProviderUpstreamCommit).In(&upstreamPath),
	).Return(&forkedProviderUpstreamCommit)
}

func upgradeProviderVersion(
	ctx Context, goMod *GoMod, target *semver.Version,
	repo ProviderRepo, targetSHA, forkedProviderUpstreamCommit string,
) step.Step {
	steps := []step.Step{}
	if goMod.Kind.IsPatched() {
		// If the provider is patched, we don't use the go module system at all. Instead
		// we update the module referenced to the new tag.
		upstreamDir := filepath.Join(repo.root, "upstream")
		steps = append(steps, step.Combined("update patched provider",
			step.Cmd(exec.CommandContext(ctx,
				"git", "submodule", "update", "--force", "--init",
			)).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "fetch", "--tags")).In(&upstreamDir),
			// We need to remove any patches to so we can cleanly pull the next upstream version.
			step.Cmd(exec.CommandContext(ctx, "git", "reset", "HEAD", "--hard")).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "checkout", "tags/v"+target.String())).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "upstream")).In(&repo.root),
			// We re-apply changes, eagerly.
			//
			// Failure to perform this step can lead to failures later, for
			// example, we might have a patched in shim dir that is not yet
			// restored, causing `go mod tidy` to fail, even where `make
			// provider` would succeed.
			step.Cmd(exec.CommandContext(ctx, "make", "upstream")).In(&repo.root),
		))
	} else if !goMod.Kind.IsForked() {
		// We have an upstream we don't control, so we need to get it's SHA. We do this
		// instead of using version tags because we can't ensure that the upstream is
		// versioning their go modules correctly.
		//
		// It they are versioning correctly, `go mod tidy` will resolve the SHA to a tag.
		steps = append(steps,
			step.F("Lookup Tag SHA", func() (string, error) {
				return runGitCommand(ctx, func(b []byte) (string, error) {
					for _, line := range strings.Split(string(b), "\n") {
						parts := strings.Split(line, "\t")
						contract.Assertf(len(parts) == 2, "expected git ls-remote to give '\t' separated values")
						if parts[1] == "refs/tags/v"+target.String() {
							return parts[0], nil
						}
					}
					return "", fmt.Errorf("could not find SHA for tag '%s'", target.Original())
				}, "ls-remote", "--tags", "https://"+modPathWithoutVersion(goMod.Upstream.Path))
			}).AssignTo(&targetSHA))
	}

	// goModDir is the directory of the go.mod where we reference the upstream provider.
	goModDir := *repo.providerDir()
	if goMod.Kind.IsShimmed() {
		// If we have a shimmed provider, we run the upstream update in the shim
		// directory, since that is what references the upstream provider.
		goModDir = filepath.Join(*repo.providerDir(), "shim")
	}

	// If a provider is patched or forked, then there is no meaningful version to
	// update. Because Go includes major versions as part of its module path, making
	// this correct can break on major version updates. We just leave it if its not
	// necessary to touch.
	if !goMod.Kind.IsPatched() && !goMod.Kind.IsForked() {
		steps = append(steps, step.Computed(func() step.Step {
			targetV := "v" + target.String()
			if targetSHA != "" {
				targetV = targetSHA
			}

			upstreamPath := goMod.Upstream.Path
			// We do this only when we already have a version suffix, since
			// that confirms that we have a correctly versioned provider.
			if indx := versionSuffix.FindStringIndex(upstreamPath); indx != nil {
				// If we have a version suffix, and we are doing a major
				// version bump, we need to apply the new suffix.
				upstreamPath = fmt.Sprintf("%s/v%d",
					upstreamPath[:indx[0]],
					target.Major())
			}

			return step.Cmd(exec.CommandContext(ctx,
				"go", "get", upstreamPath+"@"+targetV))
		}).In(&goModDir))
	}

	if goMod.Kind.IsForked() {
		// If we are running a forked update, we need to replace the reference to the fork
		// with the SHA of the new upstream branch.
		contract.Assert(forkedProviderUpstreamCommit != "")

		replaceIn := func(dir *string) {
			steps = append(steps, step.Cmd(exec.CommandContext(ctx,
				"go", "mod", "edit", "-replace",
				goMod.Fork.Old.Path+"="+
					goMod.Fork.New.Path+"@"+forkedProviderUpstreamCommit)).In(dir))
		}

		replaceIn(&goModDir)
		if goMod.Kind.IsShimmed() {
			replaceIn(repo.providerDir())
		}
	}

	if goMod.Kind.IsShimmed() {
		// When shimmed, we also run `go mod tidy` in the shim directory, and we want to
		// run that before running `go mod tidy` in the main `provider` directory.
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "mod", "tidy")).In(&goModDir))
	}

	return step.Combined("Update TF Provider", steps...)
}

func informGitHub(
	ctx Context, target UpstreamVersions, repo ProviderRepo,
	goMod *GoMod, upstreamProviderName, targetBridgeVersion string,
) step.Step {
	pushBranch := step.Cmd(exec.CommandContext(ctx, "git", "push", "--set-upstream",
		"origin", repo.workingBranch)).In(&repo.root)

	var prTitle string
	if ctx.UpgradeProviderVersion {
		prTitle = fmt.Sprintf("Upgrade terraform-provider-%s to v%s",
			upstreamProviderName, target.Latest())
	} else if ctx.UpgradeBridgeVersion {
		prTitle = "Upgrade pulumi-terraform-bridge to " + targetBridgeVersion
	} else {
		panic("Unknown action")
	}
	createPR := step.Cmd(exec.CommandContext(ctx, "gh", "pr", "create",
		"--assignee", "@me",
		"--base", repo.defaultBranch,
		"--head", repo.workingBranch,
		"--reviewer", "pulumi/Ecosystem",
		"--title", prTitle,
		"--body", prBody(ctx, repo, target, goMod, targetBridgeVersion, upstreamProviderName),
	)).In(&repo.root)
	return step.Combined("GitHub",
		pushBranch,
		createPR,
		step.Computed(func() step.Step {
			// If we are only upgrading the bridge, we wont have a list of
			// issues.
			if !ctx.UpgradeProviderVersion {
				return nil
			}

			// This PR will close issues, so we assign the issues to @me, just like
			// the PR itself.
			issues := make([]step.Step, len(target))
			for i, t := range target {
				issues[i] = step.Cmd(exec.CommandContext(ctx,
					"gh", "issue", "edit", fmt.Sprintf("%d", t.Number),
					"--add-assignee", "@me")).In(&repo.root)
			}
			return step.Combined("Self Assign Issues", issues...)
		}),
	)
}

func latestRelease(ctx context.Context, repo string) (*semver.Version, error) {
	resultBytes, err := exec.CommandContext(ctx, "gh", "repo", "view",
		repo, "--json=latestRelease").Output()
	if err != nil {
		return nil, err
	}
	var result struct {
		Latest struct {
			TagName string `json:"tagName"`
		} `json:"latestRelease"`
	}
	err = json.Unmarshal(resultBytes, &result)
	if err != nil {
		return nil, err
	}

	return semver.NewVersion(result.Latest.TagName)
}

func majorVersionBump(ctx Context, goMod *GoMod, targets UpstreamVersions, repo ProviderRepo) step.Step {
	if repo.currentVersion.Major() == 0 {
		// None of these steps are necessary or appropriate when moving from
		// version 0.x to 1.0 because Go modules only require a version suffix for
		// versions >= 2.0.
		return nil
	}

	prev := "provider"
	if repo.currentVersion.Major() > 1 {
		prev += fmt.Sprintf("/v%d", repo.currentVersion.Major())
	}

	nextMajorVersion := fmt.Sprintf("v%d", repo.currentVersion.Major()+1)

	// Replace s in file, where {} is interpolated into the old and new provider
	// component of the path.
	replaceInFile := func(desc, path, s string) step.Step {
		return updateFile(desc+" in "+path, path, func(src []byte) ([]byte, error) {
			old := strings.ReplaceAll(s, "{}", prev)
			new := strings.ReplaceAll(s, "{}", "provider/"+nextMajorVersion)

			return bytes.ReplaceAll(src, []byte(old), []byte(new)), nil
		})
	}

	name := filepath.Base(repo.root)
	return step.Combined("Increment Major Version",
		step.F("Next major version", func() (string, error) {
			// This step displays the next major version to the user.
			return nextMajorVersion, nil
		}),
		replaceInFile("Update PROVIDER_PATH", "Makefile",
			"PROVIDER_PATH := {}").In(&repo.root),
		replaceInFile("Update -X Version", ".goreleaser.yml",
			"github.com/pulumi/"+name+"/{}/pkg").In(&repo.root),
		replaceInFile("Update -X Version", ".goreleaser.prerelease.yml",
			"github.com/pulumi/"+name+"/{}/pkg").In(&repo.root),
		replaceInFile("Update Go Module", "go.mod",
			"module github.com/pulumi/"+name+"/{}").In(repo.providerDir()),
		step.F("Update Go Imports", func() (string, error) {
			var filesUpdated int
			var fn filepath.WalkFunc = func(path string, info fs.FileInfo, err error) error {
				if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
					return err
				}

				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}

				new := bytes.ReplaceAll(data,
					[]byte("github.com/pulumi/"+name+"/"+prev),
					[]byte("github.com/pulumi/"+name+"/"+"provider/"+nextMajorVersion),
				)

				if !goMod.Kind.IsPatched() && !goMod.Kind.IsForked() {
					if idx := versionSuffix.FindStringIndex(goMod.Upstream.Path); idx != nil {
						newUpstream := fmt.Sprintf("%s/v%d",
							goMod.Upstream.Path[:idx[0]],
							targets.Latest().Major(),
						)
						new = bytes.ReplaceAll(data,
							[]byte(goMod.Upstream.Path),
							[]byte(newUpstream),
						)
					}
				}

				// If the length changed, then something changed
				updated := len(data) != len(new)
				if !updated {
					// If the length stayed the same, we can check
					// each bit.
					for i := 0; i < len(data); i++ {
						if data[i] != new[i] {
							updated = true
							break
						}
					}
				}

				if updated {
					filesUpdated++
					return os.WriteFile(path, new, info.Mode().Perm())
				}
				return nil

			}
			err := filepath.Walk(*repo.providerDir(), fn)
			if err != nil {
				return "", err
			}
			err = filepath.Walk(filepath.Join(repo.root, "examples"), fn)
			return fmt.Sprintf("Updated %d files", filesUpdated), err
		}),
		step.F("info.TFProviderModuleVersion", func() (string, error) {
			b, err := os.ReadFile(filepath.Join(*repo.providerDir(), "resources.go"))
			if err != nil {
				return "", err
			}
			r, err := regexp.Compile("TFProviderModuleVersion: \"(.*)\",")
			if err != nil {
				return "", err
			}
			field := r.Find(b)
			if field == nil {
				return "not present", nil
			}
			// Escape codes are Bold, Yellow, and Reset respectively
			return "\u001B[1m\u001B[33mrequires manual update\u001B[m", nil
		}),
		step.Env("VERSION_PREFIX", repo.currentVersion.IncMajor().String()),
		addVersionPrefixToGHWorkflows(ctx, repo).In(&repo.root),
	)
}

func addVersionPrefixToGHWorkflows(ctx context.Context, repo ProviderRepo) step.Step {
	addPrefix := func(path string) error {
		stat, err := os.Stat(path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc := new(yaml.Node)
		err = yaml.Unmarshal(b, doc)
		if err != nil {
			return err
		}
		contract.Assert(doc.Kind == yaml.DocumentNode)

		// We have parsed the document node, now lets find the "env" key under it.
		var env *yaml.Node
		for _, child := range doc.Content {
			if child.Kind != yaml.MappingNode {
				continue
			}
			if child.Content[0].Value != "env" {
				continue
			}
			env = child.Content[1]
			break
		}
		if env == nil {
			// If the env node doesn't exist, we create it
			env = &yaml.Node{Kind: yaml.MappingNode}
			doc.Content = append(doc.Content, &yaml.Node{
				Kind: yaml.MappingNode,
				Content: []*yaml.Node{
					{
						Kind:  yaml.ScalarNode,
						Value: "env",
					},
					env,
				},
			})
		}

		versionPrefix := repo.currentVersion.IncMajor().String()

		var fixed bool
		for i, child := range env.Content {
			if child.Value != "VERSION_PREFIX" {
				continue
			}
			env.Content[i+1].Value = versionPrefix
			fixed = true
			break
		}

		// If we didn't find a VERSION_PREFIX node, we add one.
		if !fixed {
			env.Content = append([]*yaml.Node{
				{Value: "VERSION_PREFIX", Kind: yaml.ScalarNode},
				{Value: versionPrefix, Kind: yaml.ScalarNode},
			}, env.Content...)

		}

		updated := new(bytes.Buffer)
		enc := yaml.NewEncoder(updated)
		enc.SetIndent(2) // TODO Round trip correctly
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("Failed to marshal: %w", err)
		}
		if err := enc.Close(); err != nil {
			return fmt.Errorf("Failed to flush encoder: %w", err)
		}
		return os.WriteFile(path, updated.Bytes(), stat.Mode())
	}

	var steps []step.Step
	for _, f := range []string{"master.yml", "main.yml", "run-acceptance-tests.yml"} {
		f := filepath.Join(".github", "workflows", f)
		steps = append(steps, step.F(f, func() (string, error) {
			return "", addPrefix(f)
		}))
	}
	return step.Combined("VERSION_PREFIX workflows", steps...)
}

func updateFile(desc, path string, f func([]byte) ([]byte, error)) step.Step {
	return step.F(desc, func() (string, error) {
		stats, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		bytes, err = f(bytes)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(path, bytes, stats.Mode().Perm())
	})
}

// setCurrentUpstreamFromPatched sets repo.currentUpstreamVersion to the version pointed to in the
// submodule in the default branch.
//
// We don't use the current branch, since applying a partial update could change the current branch,
// leading to a non idempotent result.
func setCurrentUpstreamFromPatched(ctx Context, repo *ProviderRepo) error {
	getCheckedInCommit := exec.CommandContext(ctx,
		"git", "ls-tree", repo.defaultBranch, "upstream", "--object-only")
	getCheckedInCommit.Dir = repo.root

	checkedInCommit, err := getCheckedInCommit.Output()
	if err != nil {
		return err
	}
	sha := bytes.TrimSpace(checkedInCommit)

	ensureSubmoduleInit := exec.CommandContext(ctx,
		"git", "submodule", "init")
	ensureSubmoduleInit.Dir = repo.root
	out, err := ensureSubmoduleInit.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to init submodule: %w: %s", err, string(out))
	}
	getRemoteURL := exec.CommandContext(ctx,
		"git", "config", "--get", "submodule.upstream.url")
	getRemoteURL.Dir = repo.root
	remoteURLBytes, err := getRemoteURL.Output()
	if err != nil {
		return err
	}
	remoteURL := string(bytes.TrimSpace(remoteURLBytes))

	getTags := exec.CommandContext(ctx,
		"git", "ls-remote", "--tags", remoteURL)
	allTags, err := getTags.Output()
	if err != nil {
		return fmt.Errorf("failed to list remote tags for '%s': %w", remoteURL, err)
	}

	var version string
	for _, tag := range bytes.Split(allTags, []byte{'\n'}) {
		tag := bytes.TrimSpace(tag)
		if !bytes.HasPrefix(tag, sha) {
			continue
		}
		ref := string(bytes.Split(bytes.TrimSpace(tag), []byte{'\t'})[1])
		version = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ref, "refs/tags/"), "^{}"))
	}
	if version == "" {
		return fmt.Errorf("No tags match expected SHA '%s'", string(sha))
	}

	repo.currentUpstreamVersion, err = semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("current upstream version '%s': %w", version, err)
	}
	return nil
}

// setCurrentUpstreamFromPlain sets repo.currentUpstreamVersion to the version pointed to in
// provider/go.mod if the version is valid semver. Otherwise try to resolve a pseudo version against
// commits in the upstream repository.
//
// We don't use the current branch, since applying a partial update could change the current branch,
// leading to a non idempotent result.
func setCurrentUpstreamFromPlain(ctx Context, repo *ProviderRepo, goMod *GoMod) error {
	return setUpstreamFromRemoteRepo(ctx, repo, "tags",
		filepath.Join("provider", "go.mod"), goMod.Upstream.Path,
		semver.NewVersion)
}

func setCurrentUpstreamFromForked(ctx Context, repo *ProviderRepo, goMod *GoMod) error {
	return setUpstreamFromRemoteRepo(ctx, repo, "heads",
		filepath.Join("provider", "go.mod"), goMod.Fork.New.Path,
		func(s string) (*semver.Version, error) {
			version := strings.TrimPrefix(s, "upstream-")
			return semver.NewVersion(version)
		})
}

func setCurrentUpstreamFromShimmed(ctx Context, repo *ProviderRepo, goMod *GoMod) error {
	return setUpstreamFromRemoteRepo(ctx, repo, "tags",
		filepath.Join("provider", "shim", "go.mod"), goMod.Upstream.Path,
		semver.NewVersion)
}

func setUpstreamFromRemoteRepo(
	ctx Context, repo *ProviderRepo, kind, goModPath, upstream string,
	parse func(string) (*semver.Version, error),
) error {
	version, found, err := originalGoVersionOf(ctx, *repo, goModPath, upstream)
	if err != nil {
		return fmt.Errorf("could not discover original version: %w", err)
	}
	if !found {
		return fmt.Errorf("could not find previous upstream '%s'", upstream)
	}

	if !module.IsPseudoVersion(version.Version) {
		parsed, err := semver.NewVersion(version.Version)
		if err != nil {
			return fmt.Errorf("failed to parse upstream version '%s'", version.Version)
		}
		// This will not happen for pulumi forks, since we use upstream branches
		// instead of tags. It *will* happen for plain repos.
		repo.currentUpstreamVersion = parsed
		return nil
	}

	// If we don't have a fully resolved version, we got a partial version. We need to resolve
	// that back into a version tag.

	// The revision part of a go mod psuedo version generally corresponds to the commit sha1
	// that the version references.
	rev, err := module.PseudoVersionRev(version.Version)
	if err != nil {
		return fmt.Errorf("expected pseudo version, found '%s': %w", version.Version, err)
	}

	// We now fetch the set of tagged commits.
	url := "https://" + modPathWithoutVersion(upstream) + ".git"
	getTagCommits := exec.CommandContext(ctx, "git", "ls-remote", "--"+kind, "--quiet", url)
	getTagCommits.Dir = repo.root
	tagCommits, err := getTagCommits.Output()
	if err != nil {
		return fmt.Errorf("failed to get remote %s from '%s': %w", kind, url, err)
	}
	revBytes := []byte(rev)
	for _, line := range bytes.Split(tagCommits, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, revBytes) {
			continue
		}

		// It is possible that this is a different commit, since we just take the first 12
		// characters, but its **very** unlikely.
		line = bytes.Split(line, []byte{'\t'})[1]
		versionComponent := strings.TrimPrefix(string(line), "refs/"+kind+"/")
		version, err := parse(versionComponent)
		if err != nil {
			// Its possible that this error is valid, for example if the tag has a path,
			// such as 'refs/tags/sdk/v2.3.2'. If we needed this to be 100% **correct**,
			// we could require that the URL comes from a known source (`github.com`,
			// `gitlab.com`, ect.) and figure out how many tag components need to be
			// part of the url.
			//
			// It's not worth doing that for now.
			return fmt.Errorf("failed to parse commit %s '%s': %w",
				strings.TrimSuffix(kind, "s"), string(line), err)
		}
		repo.currentUpstreamVersion = version
		return nil
	}
	return fmt.Errorf("no tag commit that matched '%s' in '%s'", rev, url)
}
