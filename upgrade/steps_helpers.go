package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

func modPathWithoutVersion(path string) string {
	withoutVersion, _, ok := module.SplitPathVersion(path)
	if ok {
		return withoutVersion
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

// Find the go module version of needleModule, searching from the default repo branch, not
// the currently checked out code.
//
// originalGoVersionOfV2 performs the same function as originalGoVersionOf, except that it
// is stepv2 compatible.
var originalGoVersionOfV2 = stepv2.Func32E("Original Go Version of", func(ctx context.Context,
	repo ProviderRepo, file, needleModule string) (module.Version, bool, error) {
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
	if ctx := GetContext(ctx); ctx.UpgradeJavaVersion && ctx.JavaVersion != "" {
		var from string
		if prev := ctx.oldJavaVersion; prev != "" {
			from = fmt.Sprintf("from %s ", prev)
		}
		fmt.Fprintf(b, "- Updating Java Gen version %sto %s.\n", from, ctx.JavaVersion)
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

func gitRefsOf(ctx context.Context, url, kind string) (refs gitRepoRefs, err error) {
	err = stepv2.PipelineCtx(ctx, "shim", func(ctx context.Context) {
		refs = gitRefsOfV2(ctx, url, kind)
	})
	return
}

var gitRefsOfV2 = stepv2.Func21("git refs of", func(ctx context.Context, url, kind string) gitRepoRefs {
	out := stepv2.Cmd(ctx, "git", "ls-remote", "--"+kind, url)

	branchesToRefs := map[string]string{}
	for i, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		contract.Assertf(len(parts) == 2,
			"expected git ls-remote to give '\t' separated values, found line %d: '%s'",
			i, line)
		branchesToRefs[parts[1]] = parts[0]
	}
	return gitRepoRefs{branchesToRefs, kind}
})

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

var findCurrentMajorVersion = stepv2.Func21("Find Current Major Version",
	func(ctx context.Context, repoOrg, repoName string) *semver.Version {
		repoCurrentVersion := latestRelease(ctx, repoOrg+"/"+repoName)
		stepv2.SetLabelf(ctx, "%d", repoCurrentVersion.Major())
		return repoCurrentVersion
	},
)

var latestRelease = stepv2.Func11E("Latest Release", func(ctx context.Context, repo string) (*semver.Version, error) {
	stepv2.SetLabelf(ctx, "of %s", repo)
	resultString := stepv2.Cmd(ctx, "gh", "repo", "view",
		repo, "--json=latestRelease")
	var result struct {
		Latest struct {
			TagName string `json:"tagName"`
		} `json:"latestRelease"`
	}
	err := json.Unmarshal([]byte(resultString), &result)
	if err != nil {
		return nil, err
	}

	v, err := semver.NewVersion(result.Latest.TagName)
	if err == nil {
		stepv2.SetLabelf(ctx, "of %s: %s", repo, v)
	}

	return v, err
})

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
	repoPath = modPathWithoutVersion(repoPath)

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
var getExpectedTarget = stepv2.Func11("Get Expected Target", func(ctx context.Context,
	name string) *UpstreamUpgradeTarget {

	// we do not infer version from pulumi issues, or allow a target version when checking for a new upstream release
	if GetContext(ctx).OnlyCheckUpstream {
		return getExpectedTargetLatest(ctx)
	}

	if GetContext(ctx).TargetVersion != nil {
		target := &UpstreamUpgradeTarget{Version: GetContext(ctx).TargetVersion}

		// If we are also inferring versions, check if this PR will close any
		// issues.
		if GetContext(ctx).InferVersion {
			if fromIssues := getExpectedTargetFromIssues(ctx, name); fromIssues != nil {
				for _, issue := range fromIssues.GHIssues {
					if issue.Version != nil &&
						(issue.Version.LessThan(target.Version) ||
							issue.Version.Equal(target.Version)) {
						target.GHIssues = append(target.GHIssues, issue)
					}
				}
			}
		}
		return target
	}
	// InferVersion == true: use issue system, with ctx.TargetVersion limiting the version if set
	if GetContext(ctx).InferVersion {
		return getExpectedTargetFromIssues(ctx, name)
	}
	return getExpectedTargetLatest(ctx)
})

// getExpectedTargetLatest discovers the latest stable release and sets it on UpstreamUpgradeTarget.Version.
// There is a lot of human error and differing conventions when discovering and defining the "latest" upstream version.
// For our purposes, we always want to discover the highest, stable, valid semver, version of the upstream provider.
// We do so by listing the last 30 GitHub releases, extracting the tags from the output result (we eagerly await being
// able to get this result in json), parsing the tags into versions (filtering out any invalid or non-stable tags),
// and sorting them.
// This is a best-effort approach. There may be edge cases in which these steps do not yield the correct latest release.
var getExpectedTargetLatest = stepv2.Func01E("From Upstream Releases", func(ctx context.Context) (*UpstreamUpgradeTarget, error) {

	upstreamRepo := GetContext(ctx).UpstreamProviderOrg + "/" + GetContext(ctx).UpstreamProviderName
	// TODO: use --json once https://github.com/cli/cli/issues/4572 is fixed
	releases := stepv2.Cmd(ctx, "gh", "release", "list",
		"--repo="+upstreamRepo,
		"--exclude-drafts",
		"--exclude-pre-releases")

	resultLines := strings.Split(releases, "\n")
	// Get version tags. This will become much less laborious once we can use json.
	var tags []string
	for _, line := range resultLines {
		// split the result line by tabs - there are four fields
		fields := strings.Split(line, "\t")
		// handle empty newlines and other nonstandard output
		if len(fields) != 4 {
			continue
		}
		// the tag name for the release is the third field of the result line.
		tag := fields[2]
		tags = append(tags, tag)
	}

	// Parse tags into versions
	var versions []*semver.Version

	for _, tag := range tags {
		version, err := semver.NewVersion(tag)
		if err != nil {
			// if the version is invalid semver, we do not add it to the versions.
			// But we also do not error, because we do not want to hard-fail if there's an unusual tag lying around.
			continue
		}
		if version.Prerelease() != "" || version.Metadata() != "" {
			// we do not consider any non-stable versions.
			continue
		}
		versions = append(versions, version)
	}
	// if we did not find any valid versions, we return.
	if len(versions) == 0 {
		return nil, fmt.Errorf("no valid stable versions found in %s", upstreamRepo)
	}
	// Sort the versions.
	// Documentation here: https://pkg.go.dev/github.com/Masterminds/semver/v3#readme-sorting-semantic-versions
	sort.Sort(semver.Collection(versions))

	// our target version is the last entry in the sorted versions slice
	latestVersion := versions[len(versions)-1]
	return &UpstreamUpgradeTarget{Version: latestVersion}, nil

})

// Figure out what version of upstream to target by looking at specific pulumi-bot
// issues. These issues are created by other automation in the Pulumi GH org.
//
// This method of discovery is assumed to be specific to providers maintained by Pulumi.
var getExpectedTargetFromIssues = stepv2.Func11E("From Issues", func(ctx context.Context,
	name string) (*UpstreamUpgradeTarget, error) {
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

	return &UpstreamUpgradeTarget{
		Version:  versions[0].Version,
		GHIssues: versions,
	}, nil
})

// Create an issue in the provider repo that signals an upgrade
var createUpstreamUpgradeIssue = stepv2.Func30E("Ensure Upstream Issue", func(ctx context.Context,
	repoOrg, repoName, version string) error {
	upstreamProviderName := GetContext(ctx).UpstreamProviderName
	upstreamOrg := GetContext(ctx).UpstreamProviderOrg
	title := fmt.Sprintf("Upgrade %s to v%s", upstreamProviderName, version)

	searchIssues := stepv2.Cmd(ctx, "gh", "search", "issues",
		title,
		"--repo="+repoOrg+"/"+repoName,
		"--json=title,number",
		"--state=open",
		"--author=@me",
	)

	var issues []struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	}
	err := json.Unmarshal([]byte(searchIssues), &issues)
	if err != nil {
		return fmt.Errorf("failed to unmarshal `gh search issues` output: %w", err)
	}
	// create new issue if none exist
	createIssue := true
	// check for exact title match from search results
	for _, issue := range issues {
		if issue.Title == title {
			createIssue = false
		}
	}

	if createIssue {
		stepv2.Cmd(ctx,
			"gh", "issue", "create",
			"--repo="+repoOrg+"/"+repoName,
			"--body=Release details: https://github.com/"+upstreamOrg+"/"+upstreamProviderName+"/releases/tag/v"+version,
			"--title="+title,
			"--label="+"kind/enhancement",
		)
	}
	return nil
})
