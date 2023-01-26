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
	ok := RunSteps("Discovering Repository",
		Step("Getting Repo", func() (string, error) {
			return pulumiProviderRepo(ctx, name)
		}).AssignTo(&path),
		Step("Set default branch", func() (string, error) {
			return pullDefault(ctx, path, "origin")
		}),
		Step("Upgrade version", func() (string, error) {
			target, err := getExpectedTarget(ctx, name)
			if err == nil {
				return target.String(), nil
			}
			return "", err
		}),
		Step("Repo kind", func() (string, error) {
			goMod, err = repoKind(ctx, path, strings.TrimPrefix(name, "pulumi-"))
			if err != nil {
				return "", err
			}
			return string(goMod.Kind), nil
		}),
	)
	if !ok {
		return ErrHandled
	}
	cmdMake := func(target string) DeferredStep {
		cmd := exec.CommandContext(ctx, "make", target)
		cmd.Dir = path
		return CommandStep(cmd)
	}
	cmdGitAddAll := func() DeferredStep {
		cmd := exec.CommandContext(ctx, "git", "add --all")
		cmd.Dir = path
		return CommandStep(cmd)
	}
	cmdGitCommit := func(message string) DeferredStep {
		cmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
		cmd.Dir = path
		return CommandStep(cmd)
	}
	switch goMod.Kind {
	case Plain:
		providerPath := filepath.Join(path, "provider")
		goGetUpstream := exec.CommandContext(ctx,
			"go", "get", goMod.Upstream.Path+"@v"+target.String())
		goGetUpstream.Dir = providerPath
		goModTidy := exec.CommandContext(ctx,
			"go", "mod", "tidy")
		goModTidy.Dir = providerPath
		ok = RunSteps("Upgrading Provider",
			checkoutUpgradeBranch(ctx, path, strings.TrimPrefix(name, "pulumi-"), target),
			CommandStep(goGetUpstream),
			CommandStep(goModTidy),
			cmdMake("tfgen"),
			cmdGitAddAll(),
			cmdGitCommit("make tfgen"),
			cmdMake("build_sdks"),
			cmdGitAddAll(),
			cmdGitCommit("make build_sdks"),
		)
	case Forked:
		var upstreamPath string
		ok = RunSteps("Upgrading Forked Provider",
			Step("Ensure upstream repo", func() (string, error) {
				return ensureUpstreamRepo(ctx, goMod.Fork.Old.Path)
			}).AssignTo(&upstreamPath),
			Step("Ensure pulumi remote", func() (string, error) {
				remotes, err := runGitCommand(ctx, upstreamPath, func(b []byte) ([]string, error) {
					return strings.Split(string(b), "\n"), nil
				}, "remote")
				if err != nil {
					return "", fmt.Errorf("listing remotes: %w", err)
				}
				for _, remote := range remotes {
					if remote == "pulumi" {
						return "present", nil
					}
				}
				return runGitCommand(ctx, upstreamPath, func([]byte) (string, error) {
					return "set", nil
				}, "remote", "add",
					fmt.Sprintf("https://github.com/pulumi/terraform-provider-%s.git",
						strings.TrimPrefix(name, "pulumi-")))
			}),
		)
	case "":
		panic("Missing repo kind")
	default:
		RunSteps("Cannot upgrade " + string(goMod.Kind) + " provider")
		ok = false
	}
	if !ok {
		return ErrHandled
	}

	contract.Ignore(target)

	return nil
}

type RepoKind string

const (
	Plain            RepoKind = "plain"
	Forked                    = "forked"
	Shimmed                   = "shimmed"
	ForkedAndShimmed          = "forked & shimmed"
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

var versionSuffix = regexp.MustCompile("/v[2-9]+$")

func checkoutUpgradeBranch(
	ctx context.Context, path, name string, version *semver.Version,
) DeferredStep {
	cmd := exec.CommandContext(ctx, "git", "checkout", "--branch",
		fmt.Sprintf("upgrade-terraform-provider-%s-to-v%s", name, version))
	cmd.Dir = path
	return CommandStep(cmd)
}

type GoMod struct {
	Kind     RepoKind
	Upstream module.Version
	Fork     *modfile.Replace
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

	// Find the name of our upstream dependency
	var upstream *modfile.Require
	for _, mod := range goMod.Require {
		path := mod.Mod.Path
		pathWithoutVersion := path
		if match := versionSuffix.FindStringIndex(path); match != nil {
			pathWithoutVersion = path[:match[0]]
		}
		if strings.HasSuffix(pathWithoutVersion, tfProviderRepoName) {
			upstream = mod
			break
		}
	}

	if upstream == nil {
		return nil, fmt.Errorf("could not find upsteam in go.mod")
	}

	// If we find a replace that points to a pulumi hosted repo, that indicates a fork.
	var fork *modfile.Replace
	for _, replace := range goMod.Replace {
		// If we're not replacing our upstream, we don't care here
		if replace.Old.Path != upstream.Mod.Path {
			continue
		}
		before, after, found := strings.Cut(replace.New.Path, "/"+tfProviderRepoName)
		if !found || (after != "" && !versionSuffix.MatchString(after)) {
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

	shimDir := filepath.Join(path, "shim")
	_, err = os.Stat(shimDir)
	if err == nil {
		out.Kind = out.Kind.Shimmed()
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("unexpected error reading '%s': %w", shimDir, err)
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

func pullDefault(ctx Context, path, remote string) (string, error) {
	branches, err := runGitCommand(ctx, path, func(out []byte) ([]string, error) {
		var branches []string
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			_, ref, found := strings.Cut(line, "\t")
			contract.Assert(found)
			branch := strings.TrimPrefix(ref, "refs/heads/")
			branches = append(branches, branch)
		}
		return branches, nil
	}, "ls-remote", "--heads", remote)
	if err != nil {
		return "", fmt.Errorf("gathering branches: %w", err)
	}
	var targetBranch string
	for _, branch := range branches {
		if branch == "main" {
			targetBranch = branch
			break
		}
		if branch == "master" {
			targetBranch = branch
		}
	}
	if targetBranch == "" {
		return "", fmt.Errorf("could not find 'main' or 'master' branch in %#v", branches)
	}
	_, err = runGitCommand[struct{}](ctx, path, nil, "checkout", targetBranch)
	if err != nil {
		return "", fmt.Errorf("checkout out %s: %w", targetBranch, err)
	}
	_, err = runGitCommand[struct{}](ctx, path, nil, "pull", remote)
	if err != nil {
		return "", fmt.Errorf("fast-forwarding %s: %w", targetBranch, err)
	}
	return targetBranch, nil
}

func runGitCommand[T any](
	ctx context.Context, cwd string, filter func([]byte) (T, error), args ...string,
) (result T, err error) {
	var t T
	if cwd != "" {
		owd, err := os.Getwd()
		if err != nil {
			return t, err
		}
		defer func() {
			e := os.Chdir(owd)
			if err == nil {
				err = e
			}
		}()
		err = os.Chdir(cwd)
		if err != nil {
			return t, err
		}
	}

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

func pulumiProviderRepo(ctx Context, name string) (string, error) {
	return ensureUpstreamRepo(ctx, path.Join("github.com", "pulumi", name))
}

func downloadRepo(ctx Context, url, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", url, dst)
	return cmd.Run()
}

func ensureUpstreamRepo(ctx Context, repoPath string) (string, error) {
	// Strip version
	if match := versionSuffix.FindStringIndex(repoPath); match != nil {
		repoPath = repoPath[:match[0]]
	}

	// go from github.com/org/repo to $GOPATH/src/github.com/org
	expectedLocation := filepath.Join(strings.Split(repoPath, "/")...)
	expectedLocation = filepath.Join(ctx.GoPath, "src", expectedLocation)
	if info, err := os.Stat(expectedLocation); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("'%s' not a directory", expectedLocation)
		}
		return expectedLocation, nil
	}

	targetDir := filepath.Dir(expectedLocation)
	err := os.MkdirAll(targetDir, 0700)
	if err != nil && !os.IsExist(err) {
		return "", err
	}

	targetURL := fmt.Sprintf("https://%s.git", repoPath)
	err = downloadRepo(ctx, targetURL, expectedLocation)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", targetURL, err)
	}
	return runGitCommand(ctx, expectedLocation, func(out []byte) (string, error) {
		return expectedLocation, nil
	}, "status", "--short")
}
