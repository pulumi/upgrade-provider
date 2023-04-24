package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

var versionSuffix = regexp.MustCompile("/v[2-9][0-9]*$")

func downloadRepo(ctx Context, url, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", url, dst)
	return cmd.Run()
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

func say(msg string) func([]byte) (string, error) {
	return func([]byte) (string, error) {
		return msg, nil
	}
}

func getTfProviderRepoName(providerName string) string {
	if tfRepoName, ok := ProviderName[providerName]; ok {
		providerName = tfRepoName
	}
	return "terraform-provider-" + providerName
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

// SetCurrentUpstreamFromPlain sets repo.currentUpstreamVersion to the version pointed to in
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

// getRepoExpectedLocation checks the current working directory to see if `upgrade-provider` is being
// called from within a provider directory or subdirectory, i.e. `user/home/pulumi/pulumi-docker/provider`.
// If not, the expected location to clone the repo will be $GOPATH/src/github.com/org.
func getRepoExpectedLocation(ctx Context, cwd, repoPath string) (string, error) {
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
	expectedLocation := filepath.Join(strings.Split(repoPath, "/")...)

	tok := strings.Split(cwd, string(os.PathSeparator))
	for _, t := range tok {
		if filepath.Base(expectedLocation) == t && t != "" {
			return expectedLocation, nil
		}
	}

	return filepath.Join(ctx.GoPath, "src", expectedLocation), nil
}
