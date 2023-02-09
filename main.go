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
}

func cmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade-provider",
		Short: "upgrade-provider automatics the process of upgrading a TF-bridged provider",
		Args:  cobra.ExactArgs(1),
		Run: func(_ *cobra.Command, args []string) {
			gopath, ok := os.LookupEnv("GOPATH")
			if !ok {
				gopath = build.Default.GOPATH
			}
			context := Context{
				Context: context.Background(),
				GoPath:  gopath,
			}

			err := UpgradeProvider(context, args[0])
			if errors.Is(err, ErrHandled) {
				os.Exit(1)
			}
			if err != nil {
				fmt.Printf("error: %s\n", err.Error())
				os.Exit(1)
			}
		},
	}
}

func main() {
	cmd().Execute()
}

type HandledError struct{}

var ErrHandled = HandledError{}

func (HandledError) Error() string {
	return "Program failed and displayed the error to the user"
}

func UpgradeProvider(ctx Context, name string) error {
	var err error
	var path string
	var target *semver.Version
	var goMod *GoMod
	ok := step.Run(step.Combined("Discovering Repository",
		pulumiProviderRepos(ctx, name).AssignTo(&path),
		pullDefaultStep(ctx, "origin").In(&path),
		step.F("Upgrade version", func() (string, error) {
			target, err = getExpectedTarget(ctx, name)
			if err == nil {
				return target.String(), nil
			}
			return "", err
		}),
		step.F("Repo kind", func() (string, error) {
			goMod, err = repoKind(ctx, path, strings.TrimPrefix(name, "pulumi-"))
			if err != nil {
				return "", err
			}
			return string(goMod.Kind), nil
		}),
	))
	if !ok {
		return ErrHandled
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() {
		var upstreamPath string
		var previousUpstreamVersion *semver.Version
		ok = step.Run(step.Combined("Upgrading Forked Provider",
			ensureUpstreamRepo(ctx, goMod.Fork.Old.Path).AssignTo(&upstreamPath),
			step.F("ensure pulumi remote", func() (string, error) {
				return ensurePulumiRemote(ctx, strings.TrimPrefix(name, "pulumi-"))
			}).In(&upstreamPath),
			step.Cmd(exec.Command("git", "fetch", "pulumi")).In(&upstreamPath),
			step.F("discover previous upstream version", func() (string, error) {
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
			step.F("checkout upstream", func() (string, error) {
				return runGitCommand(ctx,
					func([]byte) (string, error) { return "", nil },
					"checkout", fmt.Sprintf("pulumi/upstream-v%s", previousUpstreamVersion))
			}).In(&upstreamPath),
			step.F("upstream branch", func() (string, error) {
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
			step.F("merge upstream branch", func() (string, error) {
				return runGitCommand(ctx, say("no conflict"),
					"merge", "v"+target.String())
			}).In(&upstreamPath),
			step.Cmd(exec.CommandContext(ctx, "go", "build", ".")).In(&upstreamPath),
			step.F("push upstream", func() (string, error) {
				return runGitCommand(ctx, noOp,
					"push", "pulumi", "upstream-v"+target.String())
			}).In(&upstreamPath),
			step.F("get head commit", func() (string, error) {
				return runGitCommand(ctx, func(b []byte) (string, error) {
					return strings.TrimSpace(string(b)), nil
				}, "rev-parse", "HEAD")
			}).AssignTo(&forkedProviderUpstreamCommit).In(&upstreamPath),
		))
		if !ok {
			return ErrHandled
		}
	}
	var targetSHA string
	providerPath := filepath.Join(path, "provider")
	branchName := fmt.Sprintf("upgrade-terraform-provider-%s-to-v%s", strings.TrimPrefix(name, "pulumi-"), target)
	steps := []step.Step{
		step.F("ensure branch", func() (string, error) {
			return ensureBranchCheckedOut(ctx, branchName)
		}).In(&path),
		step.Cmd(exec.CommandContext(ctx,
			"go", "get", "-u", "github.com/pulumi/pulumi-terraform-bridge/v3")).In(&providerPath),
	}
	if goMod.Kind.IsPatched() {
		// If the provider is patched, we don't use the go module system at all. Instead
		// we update the module referenced to the new tag.
		upstreamDir := filepath.Join(path, "upstream")
		steps = append(steps, step.Combined("update patched provider",
			step.Cmd(exec.CommandContext(ctx, "git", "fetch", "--tags")).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "checkout", "tags/v"+target.String())).In(&upstreamDir),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "upstream")).In(&path),
		))
	} else if !goMod.Kind.IsForked() {
		// We have an upstream we don't control, so we need to git it's SHA. We do this
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
	goModDir := providerPath
	if goMod.Kind.IsShimmed() {
		// If we have a shimmed provider, we run the upstream update in the shim
		// directory, since that is what references the upstream provider.
		goModDir = filepath.Join(providerPath, "shim")
	}
	if !goMod.Kind.IsPatched() {
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
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "mod", "edit", "-replace",
			goMod.Fork.Old.Path+"="+
				goMod.Fork.New.Path+"@"+forkedProviderUpstreamCommit)).In(&goModDir))
	}

	if goMod.Kind.IsShimmed() {
		// When shimmed, we also run `go mod tidy` in the outer directory.
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "mod", "tidy")).In(&providerPath))
	}

	ok = step.Run(step.Combined("Upgrading Provider",
		append(steps,
			step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(&providerPath),
			step.Cmd(exec.CommandContext(ctx, "pulumi", "plugin", "rm", "--all", "--yes")),
			step.Cmd(exec.CommandContext(ctx, "make", "tfgen")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make tfgen")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "make", "build_sdks")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make build_sdks")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branchName)).In(&path),
		)...))
	if !ok {
		return ErrHandled
	}

	contract.Ignore(target)

	return nil
}

type RepoKind string

const (
	Plain             RepoKind = "plain"
	Forked                     = "forked"
	Shimmed                    = "shimmed"
	ForkedAndShimmed           = "forked & shimmed"
	Patched                    = "patched"
	PatchedAndShimmed          = "patched & shimmed"
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

func ensureBranchCheckedOut(ctx Context, branchName string) (string, error) {
	branchExists, err := runGitCommand(ctx, func(b []byte) (bool, error) {
		lines := strings.Split(string(b), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == branchName {
				return true, nil
			}
		}
		return false, nil
	}, "branch")
	if err != nil {
		return "", err
	}
	if !branchExists {
		return runGitCommand(ctx, say("creating "+branchName),
			"checkout", "-b", branchName)
	}
	return runGitCommand(ctx, say("switching to "+branchName),
		"checkout", branchName)
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

func repoKind(ctx context.Context, path, providerName string) (*GoMod, error) {
	file := filepath.Join(path, "provider", "go.mod")

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}

	goMod, err := modfile.Parse(file, data, nil)
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}
	tfProviderRepoName := "terraform-provider-" + providerName

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

func getExpectedTarget(ctx context.Context, name string) (*semver.Version, error) {
	getIssues := exec.CommandContext(ctx, "gh", "issue", "list",
		"--state=open",
		"--author=pulumi-bot",
		"--repo=pulumi/"+name,
		"--limit=100",
		"--json=title")
	bytes := new(bytes.Buffer)
	getIssues.Stdout = bytes
	err := getIssues.Run()
	if err != nil {
		return nil, err
	}
	titles := []struct {
		Title string `json:"title"`
	}{}
	err = json.Unmarshal(bytes.Bytes(), &titles)
	if err != nil {
		return nil, err
	}
	var versions []*semver.Version
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
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no upgrade found")
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[j].LessThan(versions[i])
	})
	return versions[0], nil
}

func pullDefaultStep(ctx Context, remote string) step.Step {
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
func noOp([]byte) (string, error) { return "", nil }
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
			return expectedLocation, exec.CommandContext(ctx, "git", "status", "--short").Run()
		}).In(&expectedLocation),
	)
}
