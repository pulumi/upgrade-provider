package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/spf13/cobra"

	"github.com/pulumi/upgrade-provider/step"
)

type Context struct {
	context.Context

	GoPath string

	MaxVersion *semver.Version

	UpgradeBridgeVersion   bool
	UpgradeProviderVersion bool
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
}

func (p ProviderRepo) providerDir() *string {
	dir := filepath.Join(p.root, "provider")
	return &dir
}

func UpgradeProvider(ctx Context, name string) error {
	var err error
	var repo ProviderRepo
	var targetBridgeVersion string
	var upgradeTargets []UpgradeTargetIssue
	var target *semver.Version
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

	if ctx.UpgradeProviderVersion {
		discoverSteps = append(discoverSteps,
			step.F("Target Provider Version", func() (string, error) {
				upgradeTargets, err = getExpectedTarget(ctx, name)
				if err == nil {
					target = upgradeTargets[0].Version
					return upgradeTargets[0].Version.String(), nil
				}
				return "", err
			}))
	}

	discoverSteps = append(discoverSteps, step.F("Repo kind", func() (string, error) {
		goMod, err = repoKind(ctx, repo, upstreamProviderName)
		if err != nil {
			return "", err
		}
		return string(goMod.Kind), nil
	}))

	if ctx.UpgradeBridgeVersion {
		discoverSteps = append(discoverSteps,
			step.F("Target Bridge Version", func() (string, error) {
				resultBytes, err := exec.CommandContext(ctx, "gh", "repo", "view",
					"pulumi/pulumi-terraform-bridge", "--json=latestRelease").Output()
				if err != nil {
					return "", err
				}
				result := map[string]any{}
				err = json.Unmarshal(resultBytes, &result)
				if err != nil {
					return "", err
				}
				return result["latestRelease"].(map[string]any)["tagName"].(string), nil
			}).AssignTo(&targetBridgeVersion))
	}

	ok = step.Run(step.Combined("Discovering Repository", discoverSteps...))
	if !ok {
		return ErrHandled
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() && ctx.UpgradeBridgeVersion {
		ok = step.Run(upgradeUpstreamFork(ctx, name, target, goMod).
			AssignTo(&forkedProviderUpstreamCommit))
		if !ok {
			return ErrHandled
		}
	}

	var targetSHA string
	if ctx.UpgradeProviderVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-terraform-provider-%s-to-v%s",
			upstreamProviderName, target)
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
		step.Computed(func() step.Step {
			if goMod.Kind.IsPatched() {
				return step.Cmd(exec.CommandContext(ctx, "make", "upstream")).In(&repo.root)
			}
			return nil
		}),
	}
	if ctx.UpgradeBridgeVersion {
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "get", "github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion)).
			In(repo.providerDir()))
	}

	if ctx.UpgradeProviderVersion {
		steps = append(steps, upgradeProviderVersion(ctx, goMod, target, repo,
			targetSHA, forkedProviderUpstreamCommit))
	}

	ok = step.Run(step.Combined("Update Artifacts",
		append(steps,
			step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(repo.providerDir()),
			step.Cmd(exec.CommandContext(ctx, "pulumi", "plugin", "rm", "--all", "--yes")),
			step.Cmd(exec.CommandContext(ctx, "make", "tfgen")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make tfgen")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "make", "build_sdks")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make build_sdks", "--allow-empty")).In(&repo.root),
			informGitHub(ctx, target, upgradeTargets,
				repo, upstreamProviderName, targetBridgeVersion),
		)...))
	if !ok {
		return ErrHandled
	}

	return nil
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

var versionSuffix = regexp.MustCompile("/v[2-9]+$")

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
}

func modPathWithoutVersion(path string) string {
	if match := versionSuffix.FindStringIndex(path); match != nil {
		return path[:match[0]]
	}
	return path
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

func getExpectedTarget(ctx Context, name string) ([]UpgradeTargetIssue, error) {
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
		return nil, err
	}
	titles := []struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	}{}
	err = json.Unmarshal(bytes.Bytes(), &titles)
	if err != nil {
		return nil, err
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
			extra = "(a version was found but it was greater then the specified max)"
		}
		return nil, fmt.Errorf("no upgrade found%s", extra)
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[j].Version.LessThan(versions[i].Version)
	})

	if ctx.MaxVersion != nil && !versions[0].Version.Equal(ctx.MaxVersion) {
		return nil, fmt.Errorf("possible upgrades exist, but non match %s", ctx.MaxVersion)
	}

	return versions, nil
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

			// go from github.com/org/repo to $GOPATH/src/github.com/org
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

func prBody(upgradeTargets []UpgradeTargetIssue) string {
	b := new(strings.Builder)
	fmt.Fprintf(b, "This PR was generated by the upgrade-provider tool.\n")

	fmt.Fprintf(b, "\n---\n\n")
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
			step.Cmd(exec.CommandContext(ctx, "git", "fetch", "--tags")).In(&upstreamDir),
			// We need to remove any patches to so we can cleanly pull the next upstream version.
			step.Cmd(exec.CommandContext(ctx, "git", "reset", "HEAD", "--hard")).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "checkout", "tags/v"+target.String())).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "upstream")).In(&repo.root),
			// We re-apply changes, eagerly.
			//
			// Failure to perform this step can lead to failures later, for
			// example, wee might have a patched in shim dir that is not yet
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
			target := "v" + target.String()
			if targetSHA != "" {
				target = targetSHA
			}
			return step.Cmd(exec.CommandContext(ctx,
				"go", "get", goMod.Upstream.Path+"@"+target))
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
	ctx Context, target *semver.Version, upgradeTargets []UpgradeTargetIssue,
	repo ProviderRepo, upstreamProviderName, targetBridgeVersion string,
) step.Step {
	pushBranch := step.Cmd(exec.CommandContext(ctx, "git", "push", "--set-upstream",
		"origin", repo.workingBranch)).In(&repo.root)

	var prTitle string
	if ctx.UpgradeProviderVersion {
		prTitle = fmt.Sprintf("Upgrade terraform-provider-%s to v%s",
			upstreamProviderName, target)
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
		"--body", prBody(upgradeTargets),
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
			issues := make([]step.Step, len(upgradeTargets))
			for i, t := range upgradeTargets {
				issues[i] = step.Cmd(exec.CommandContext(ctx,
					"gh", "issue", "edit", fmt.Sprintf("%d", t.Number),
					"--add-assignee", "@me")).In(&repo.root)
			}
			return step.Combined("Self Assign Issues", issues...)
		}),
	)
}
