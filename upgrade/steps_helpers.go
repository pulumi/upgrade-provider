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
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

var versionSuffix = regexp.MustCompile("/v[2-9][0-9]*$")

func modPathWithoutVersion(path string) string {
	if match := versionSuffix.FindStringIndex(path); match != nil {
		return path[:match[0]]
	}
	return path
}

// Find the go module version of needleModule, searching from the default repo branch, not
// the currently checked out code.
func originalGoVersionOf(ctx context.Context, repo ProviderRepo, file, needleModule string) (module.Version, bool, error) {
	data, err := baseFileAt(ctx, repo, file)
	if err != nil {
		return module.Version{}, false, err
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

var originalGoVersionOfV2 = stepv2.Func32E("Original Go Version of", func(ctx context.Context, repo ProviderRepo, file, needleModule string) (module.Version, bool, error) {
	data := baseFileAtV2(ctx, repo, file)
	goMod, err := modfile.Parse(file, []byte(data), nil)
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
})

// Look up the version of the go dependency requirement of a given module in a given modfile.
func currentGoVersionOf(modFile, lookupModule string) (module.Version, bool, error) {
	fileData, err := os.ReadFile(modFile)
	if err != nil {
		return module.Version{}, false, err
	}
	goMod, err := modfile.Parse(modFile, fileData, nil)
	if err != nil {
		return module.Version{}, false, fmt.Errorf("%s: %w",
			modFile, err)
	}

	// We can only look up requirements in this way.
	for _, replacement := range goMod.Replace {
		if replacement.Old.Path == lookupModule {
			return module.Version{}, false, fmt.Errorf(
				"module %s is being replaced, cannot lookup version", lookupModule,
			)
		}
	}

	for _, requirement := range goMod.Require {
		if requirement.Mod.Path == lookupModule {
			return requirement.Mod, true, nil
		}
	}
	return module.Version{}, false, nil
}

func baseFileAt(ctx context.Context, repo ProviderRepo, file string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "show", repo.defaultBranch+":"+file)
	cmd.Dir = repo.root
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s:%s: %w", repo.defaultBranch, file, err)
	}
	return data, nil
}

func baseFileAtV2(ctx context.Context, repo ProviderRepo, file string) string {
	ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: repo.root})
	return stepv2.Cmd(ctx, "git", "show", repo.defaultBranch+":"+file)
}

func prBody(ctx context.Context, repo ProviderRepo,
	upgradeTarget *UpstreamUpgradeTarget, goMod *GoMod,
	targetBridge, targetPf Ref, tfSDKUpgrade string, osArgs []string) string {
	b := new(strings.Builder)

	// We strip out --pr-description since it will appear later in the pr body.
	for i, v := range osArgs {
		if v == "--pr-description" {
			osArgs = append(osArgs[:i], osArgs[i+2:]...)
			break
		} else if strings.HasPrefix(v, "--pr-description=") {
			osArgs = append(osArgs[:i], osArgs[i+1:]...)
			break
		}
	}
	argsSpliced := strings.Join(osArgs[1:], " ")

	fmt.Fprintf(b, "This PR was generated via `$ upgrade-provider %s`.\n", argsSpliced)

	fmt.Fprintf(b, "\n---\n\n")

	if GetContext(ctx).MajorVersionBump {
		fmt.Fprintf(b, "- Updating major version from %s to %s.\n", repo.currentVersion, repo.currentVersion.IncMajor())
	}
	if ctx := GetContext(ctx); ctx.oldJavaVersion != ctx.JavaVersion && ctx.JavaVersion != "" {
		var from string
		if prev := ctx.oldJavaVersion; prev != "" {
			from = fmt.Sprintf("from %s ", prev)
		}
		fmt.Fprintf(b, "- Updating java version %sto %s.\n", from, ctx.JavaVersion)
	}

	if GetContext(ctx).UpgradeProviderVersion {
		contract.Assertf(upgradeTarget != nil, "upgradeTarget should always be non-nil")
		var prev string
		if repo.currentUpstreamVersion != nil {
			prev = fmt.Sprintf("from %s ", repo.currentUpstreamVersion)
		}
		fmt.Fprintf(b, "- Upgrading %s %s to %s.\n",
			GetContext(ctx).UpstreamProviderName, prev, upgradeTarget.Version)
		for _, t := range upgradeTarget.GHIssues {
			if t.Number > 0 {
				fmt.Fprintf(b, "\tFixes #%d\n", t.Number)
			}
		}
	}
	if GetContext(ctx).UpgradeBridgeVersion {
		fmt.Fprintf(b, "- Upgrading pulumi-terraform-bridge from %s to %s.\n",
			goMod.Bridge.Version, targetBridge)
	}
	if GetContext(ctx).UpgradePfVersion {
		fmt.Fprintf(b, "- Upgrading pulumi-terraform-bridge/pf from %s to %v.\n",
			goMod.Pf.Version, targetPf)
	}

	if parts := strings.Split(tfSDKUpgrade, " -> "); len(parts) == 2 {
		fmt.Fprintf(b, "- Upgrading pulumi/terraform-plugin-sdk from %s to %s.\n",
			parts[0], parts[1])
	}

	if d := GetContext(ctx).PRDescription; d != "" {
		fmt.Fprintf(b, "\n\n%s\n\n", d)
	}

	return b.String()
}

var ensurePulumiRemote = stepv2.Func10("Ensure Pulumi Remote", func(ctx context.Context, name string) {
	remotes := strings.Split(stepv2.Cmd(ctx, "git", "remote"), "\n")
	for _, remote := range remotes {
		if remote == "pulumi" {
			stepv2.SetLabel(ctx, "remote 'pulumi' already exists")
			return
		}
	}

	stepv2.Cmd(ctx, "git", "remote", "add", "pulumi",
		fmt.Sprintf("https://github.com/pulumi/terraform-provider-%s.git", name))
	stepv2.SetLabel(ctx, "remote set to 'pulumi'")
})

// setCurrentUpstreamFromPatched sets repo.currentUpstreamVersion to the version pointed to in the
// submodule in the default branch.
//
// We don't use the current branch, since applying a partial update could change the current branch,
// leading to a non idempotent result.
var setCurrentUpstreamFromPatched = stepv2.Func10E("Set Upstream From Patched", func(ctx context.Context,
	repo *ProviderRepo) error {
	ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: repo.root})
	checkedInCommit := stepv2.Cmd(ctx,
		"git", "ls-tree", repo.defaultBranch, "upstream", "--object-only")
	sha := strings.TrimSpace(checkedInCommit)

	stepv2.Cmd(ctx, "git", "submodule", "init")
	remoteURL := strings.TrimSpace(stepv2.Cmd(ctx,
		"git", "config", "--get", "submodule.upstream.url"))

	allTags := stepv2.Cmd(ctx,
		"git", "ls-remote", "--tags", remoteURL)

	var version string
	for _, tag := range strings.Split(allTags, "\n") {
		tag := strings.TrimSpace(tag)
		if !strings.HasPrefix(tag, sha) {
			continue
		}
		ref := strings.Split(strings.TrimSpace(tag), "\t")[1]
		version = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ref, "refs/tags/"), "^{}"))
	}
	if version == "" {
		return fmt.Errorf("No tags match expected SHA '%s'", string(sha))
	}

	var err error
	repo.currentUpstreamVersion, err = semver.NewVersion(version)
	if err != nil {
		return fmt.Errorf("current upstream version '%s': %w", version, err)
	}
	return nil
})

// SetCurrentUpstreamFromPlain sets repo.currentUpstreamVersion to the version pointed to in
// provider/go.mod if the version is valid semver. Otherwise try to resolve a pseudo version against
// commits in the upstream repository.
//
// We don't use the current branch, since applying a partial update could change the current branch,
// leading to a non idempotent result.
func setCurrentUpstreamFromPlain(ctx context.Context, repo *ProviderRepo, goMod *GoMod) {
	f := stepv2.Func50E("Set Current Upstream From Plain", setUpstreamFromRemoteRepo)
	f(ctx, repo, "tags", filepath.Join("provider", "go.mod"), goMod.Upstream.Path,
		semver.NewVersion)
}

func setCurrentUpstreamFromForked(ctx context.Context, repo *ProviderRepo, goMod *GoMod) {
	f := stepv2.Func50E("Set Current Upstream From Forked", setUpstreamFromRemoteRepo)
	f(ctx, repo, "heads", filepath.Join("provider", "go.mod"), goMod.Fork.New.Path,
		func(s string) (*semver.Version, error) {
			version := strings.TrimPrefix(s, "upstream-")
			return semver.NewVersion(version)
		})
}

func setCurrentUpstreamFromShimmed(ctx context.Context, repo *ProviderRepo, goMod *GoMod) {
	f := stepv2.Func50E("Set Current Upstream From Shimmed", setUpstreamFromRemoteRepo)
	f(ctx, repo, "tags", filepath.Join("provider", "shim", "go.mod"), goMod.Upstream.Path,
		semver.NewVersion)
}

func setUpstreamFromRemoteRepo(
	ctx context.Context, repo *ProviderRepo, kind, goModPath, upstream string,
	parse func(string) (*semver.Version, error),
) error {
	version, found := originalGoVersionOfV2(ctx, *repo, goModPath, upstream)
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
	var tagCommits string
	stepv2.WithCwd(ctx, repo.root, func(ctx context.Context) {
		tagCommits = stepv2.Cmd(ctx, "git", "ls-remote", "--"+kind, "--quiet", url)
	})
	for _, line := range strings.Split(tagCommits, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, rev) {
			continue
		}

		// It is possible that this is a different commit, since we just take the first 12
		// characters, but its **very** unlikely.
		line = strings.Split(line, "\t")[1]
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

func gitRefsOf(ctx context.Context, url, kind string) (gitRepoRefs, error) {
	args := []string{"ls-remote", "--" + kind, url}
	cmd := exec.CommandContext(ctx, "git", args...)
	out := new(bytes.Buffer)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return gitRepoRefs{}, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	branchesToRefs := map[string]string{}
	for i, line := range strings.Split(out.String(), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		contract.Assertf(len(parts) == 2,
			"expected git ls-remote to give '\t' separated values, found line %d: '%s'",
			i, line)
		branchesToRefs[parts[1]] = parts[0]
	}
	return gitRepoRefs{branchesToRefs, kind}, nil

}

type gitRepoRefs struct {
	labelToRef map[string]string
	kind       string
}

func (g gitRepoRefs) shaOf(label string) (string, bool) {
	for l, ref := range g.labelToRef {
		if l == label {
			return ref, true
		}
	}
	return "", false
}

func (g gitRepoRefs) labelOf(sha string) (string, bool) {
	for label, ref := range g.labelToRef {
		if strings.HasPrefix(ref, sha) {
			return label, true
		}
	}
	return "", false
}

func (g gitRepoRefs) sortedLabels(less func(string, string) bool) []string {
	labels := make([]string, 0, len(g.labelToRef))
	for label := range g.labelToRef {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		return less(labels[i], labels[j])
	})
	return labels
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

func getGitHubPath(repoPath string) (string, error) {
	if prefix, repo, found := strings.Cut(repoPath, "/terraform-providers/"); found {
		name := strings.TrimPrefix(repo, "terraform-provider-")
		org, ok := ProviderOrgs[name]
		if !ok {
			return "", fmt.Errorf("terraform-providers based path: missing remap for '%s'", name)
		}
		repoPath = prefix + "/" + org + "/" + repo
	}
	return repoPath, nil
}

// getRepoExpectedLocation will return one of the following:
// 1) --repo-path: if set, returns the specified repo path
// 2) current working directory: returns the path to the cwd if it is a provider directory
// or subdirectory, i.e. `user/home/pulumi/pulumi-docker/provider` it
// 3) default: $GOPATH/src/module, i.e. $GOPATH/src/github.com/pulumi/pulumi-datadog
func getRepoExpectedLocation(ctx context.Context, cwd, repoPath string) (string, error) {
	// We assume the user passed in a valid path, either absolute or relative.
	if path := GetContext(ctx).repoPath; path != "" {
		return path, nil
	}

	// Strip version
	if match := versionSuffix.FindStringIndex(repoPath); match != nil {
		repoPath = repoPath[:match[0]]
	}

	repoPath, err := getGitHubPath(repoPath)
	if err != nil {
		return "", fmt.Errorf("repo location: %w", err)
	}

	// from github.com/org/repo to $GOPATH/src/github.com/org
	expectedLocation := filepath.Join(strings.Split(repoPath, "/")...)

	expectedBase := filepath.Base(expectedLocation)

	for cwd != "" && cwd != string(os.PathSeparator) && cwd != "." {
		if filepath.Base(cwd) == expectedBase {
			return cwd, nil
		}
		cwd = filepath.Dir(cwd)
	}

	return filepath.Join(GetContext(ctx).GoPath, "src", expectedLocation), nil
}

// Fetch the expected upgrade target from github. Return a list of open upgrade issues,
// sorted by semantic version. The list may be empty.
//
// The second argument represents a message to describe the result. It may be empty.
var getExpectedTarget = stepv2.Func21("Get Expected Target", func(ctx context.Context, name, upstreamOrg string) *UpstreamUpgradeTarget {
	// InferVersion == true: use issue system, with ctx.TargetVersion limiting the version if set
	if GetContext(ctx).InferVersion {
		return getExpectedTargetFromIssues(ctx, name)
	}
	if GetContext(ctx).TargetVersion != nil {
		return &UpstreamUpgradeTarget{Version: GetContext(ctx).TargetVersion}

	}
	return getExpectedTargetLatest(ctx, name, upstreamOrg)
})

var getExpectedTargetLatest = stepv2.Func21E("From Upstream Releases", func(ctx context.Context,
	name, upstreamOrg string) (*UpstreamUpgradeTarget, error) {
	latest := stepv2.Cmd(ctx, "gh", "release", "list",
		"--repo="+upstreamOrg+"/"+GetContext(ctx).UpstreamProviderName,
		"--limit=1",
		"--exclude-drafts",
		"--exclude-pre-releases")

	tok := strings.Fields(latest)
	contract.Assertf(len(tok) > 0, fmt.Sprintf("no releases found in %s/%s",
		upstreamOrg, GetContext(ctx).UpstreamProviderName))
	v, err := semver.NewVersion(tok[0])
	if err != nil {
		return nil, err
	}
	return &UpstreamUpgradeTarget{Version: v}, nil
})

var getExpectedTargetFromIssues = stepv2.Func11E("From Issues", func(ctx context.Context,
	name string) (*UpstreamUpgradeTarget, error) {
	target := &UpstreamUpgradeTarget{}
	issueList := stepv2.Cmd(ctx, "gh", "issue", "list",
		"--state=open",
		"--author=pulumi-bot",
		"--repo="+name,
		"--limit=100",
		"--json=title,number")
	titles := []struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	}{}
	err := json.Unmarshal([]byte(issueList), &titles)
	if err != nil {
		return nil, err
	}

	var versions []UpgradeTargetIssue
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
		if err != nil {
			continue
		}
		versions = append(versions, UpgradeTargetIssue{
			Version: v,
			Number:  title.Number,
		})
	}
	if len(versions) == 0 {
		return nil, nil
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[j].Version.LessThan(versions[i].Version)
	})

	if ctx := GetContext(ctx); ctx.TargetVersion != nil {
		var foundTarget bool
		for i, v := range versions {
			if v.Version.Equal(ctx.TargetVersion) {
				// Change the target version to be the latest that we
				// found.
				versions = versions[i:]
				foundTarget = true
				break
			}
		}
		if !foundTarget {
			return nil, fmt.Errorf("possible upgrades exist, but none match %s", ctx.TargetVersion)
		}
	}

	target.GHIssues = versions
	target.Version = versions[0].Version
	return target, nil
})
