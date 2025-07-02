package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

func prTitle(ctx context.Context, target *UpstreamUpgradeTarget, targetBridgeVersion Ref) (string, error) {
	c := GetContext(ctx)
	title := c.PRTitlePrefix

	switch {
	case c.UpgradeProviderVersion:
		title += fmt.Sprintf("Upgrade %s to v%s", c.UpstreamProviderName, target.Version)
	case c.UpgradeBridgeVersion:
		title += "Upgrade pulumi-terraform-bridge to " + targetBridgeVersion.String()
	case c.TargetPulumiVersion != nil:
		title += "Test: Upgrade pulumi/{pkg,sdk} to " + c.TargetPulumiVersion.String()
	case c.UpgradeJavaVersion:
		title += "Upgrade pulumi-java to " + c.JavaVersion
	default:
		return "", fmt.Errorf("unknown action")
	}

	return title, nil
}

func prBody(ctx context.Context, repo ProviderRepo,
	upgradeTarget *UpstreamUpgradeTarget, goMod *GoMod,
	targetBridge Ref, tfSDKUpgrade string, osArgs []string,
) string {
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
		prev := fmt.Sprintf("from %s ", repo.currentUpstreamVersion)
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

	if parts := strings.Split(tfSDKUpgrade, " -> "); len(parts) == 2 {
		fmt.Fprintf(b, "- Upgrading pulumi/terraform-plugin-sdk from %s to %s.\n",
			parts[0], parts[1])
	}

	if d := GetContext(ctx).PRDescription; d != "" {
		fmt.Fprintf(b, "\n\n%s\n\n", d)
	}

	return b.String()
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

var findCurrentMajorVersion = stepv2.Func21E("Find Current Major Version",
	func(ctx context.Context, repoOrg, repoName string) (*semver.Version, error) {
		repoCurrentVersion, ok := latestReleaseVersion(ctx, repoOrg+"/"+repoName)
		if !ok {
			return nil, fmt.Errorf("could not find an existing release")
		}
		stepv2.SetLabelf(ctx, "%d", repoCurrentVersion.Major())
		return repoCurrentVersion, nil
	},
)

type releaseInfo struct {
	Latest *struct {
		TagName     string `json:"tagName"`
		PublishedAt string `json:"publishedAt"`
	} `json:"latestRelease"`
}

func latestReleaseInfo(ctx context.Context, repo string) (releaseInfo, error) {
	var info releaseInfo
	resultString := stepv2.Cmd(ctx, "gh", "repo", "view",
		repo, "--json=latestRelease")
	err := json.Unmarshal([]byte(resultString), &info)

	return info, err
}

var latestReleaseVersion = stepv2.Func12E("Latest Release Version",
	func(ctx context.Context, repo string) (*semver.Version, bool, error) {
		stepv2.SetLabelf(ctx, "of %s", repo)
		rel, err := latestReleaseInfo(ctx, repo)
		if err != nil {
			return nil, false, err
		}
		if rel.Latest == nil {
			return nil, false, nil
		}
		v, err := semver.NewVersion(rel.Latest.TagName)
		if err == nil {
			stepv2.SetLabelf(ctx, "of %s: %s", repo, v)
		}
		return v, true, err
	})

// getExpectedTargetLatest discovers the latest stable release and sets it on UpstreamUpgradeTarget.Version.
// There is a lot of human error and differing conventions when discovering and defining the "latest" upstream version.
// For our purposes, we always want to discover the highest, stable, valid semver, version of the upstream provider.
// We do so by listing the last 30 GitHub releases, extracting the tags from the output result (we eagerly await being
// able to get this result in json), parsing the tags into versions (filtering out any invalid or non-stable tags),
// and sorting them.
// This is a best-effort approach. There may be edge cases in which these steps do not yield the correct latest release.
func getExpectedTargetLatest(ctx context.Context) (*UpstreamUpgradeTarget, error) {
	upstreamRepo := GetContext(ctx).UpstreamProviderOrg + "/" + GetContext(ctx).UpstreamProviderName
	// TODO: use --json once https://github.com/cli/cli/issues/4572 is fixed
	c := GetContext(ctx)
	releases := c.r.Run(
		[]string{
			"gh", "release", "list",
			"--repo=" + upstreamRepo,
			"--exclude-drafts",
			"--exclude-pre-releases",
		},
	)

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
}

// Get a list of open issues for a given provider which will be closed by the upgrade PR.
var getIssueList = stepv2.Func11E("From Issues", func(ctx context.Context, name string) ([]UpgradeTargetIssue, error) {
	issueList := stepv2.Cmd(ctx, "gh", "issue", "list",
		"--state=open",
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

	var upgradeTargetIssues []UpgradeTargetIssue
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
		upgradeTargetIssues = append(upgradeTargetIssues, UpgradeTargetIssue{
			Version: v,
			Number:  title.Number,
		})
	}
	if len(upgradeTargetIssues) == 0 {
		return nil, nil
	}
	sort.Slice(upgradeTargetIssues, func(i, j int) bool {
		return upgradeTargetIssues[j].Version.LessThan(upgradeTargetIssues[i].Version)
	})

	return upgradeTargetIssues, nil
})

// Hide searchable token in the issue body via an HTML comment to help us find this issue later without requiring labels to be set up.
const (
	upgradeIssueToken        = "pulumiupgradeproviderissue"
	upgradeIssueBodyTemplate = `
<!-- for upgrade-provider issue searching: pulumiupgradeproviderissue -->

> [!NOTE]
> This issue was created automatically by the upgrade-provider tool and should be automatically closed by a subsequent upgrade pull request.
`
)

// Create an issue in the provider repo that signals an upgrade
var createUpstreamUpgradeIssue = stepv2.Func30E("Ensure Upstream Issue", func(ctx context.Context,
	repoOrg, repoName, version string,
) error {
	upstreamProviderName := GetContext(ctx).UpstreamProviderName
	upstreamOrg := GetContext(ctx).UpstreamProviderOrg
	title := fmt.Sprintf("Upgrade %s to v%s", upstreamProviderName, version)

	issueAlreadyExists, err := upgradeIssueExits(ctx, title, repoOrg, repoName)
	if err != nil {
		return err
	}

	// Write issue_created=true to GITHUB_OUTPUT, if it exists for CI control flow.
	if GITHUB_OUTPUT, found := os.LookupEnv("GITHUB_OUTPUT"); found {
		f, err := os.OpenFile(GITHUB_OUTPUT, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "issue_created=true\n"); err != nil {
			return err
		}
	}

	// We've found an appropriate existing issue, so we'll skip creating a new one.
	if issueAlreadyExists {
		return nil
	}

	stepv2.Cmd(ctx,
		"gh", "issue", "create",
		"--repo="+repoOrg+"/"+repoName,
		"--body=Release details: https://github.com/"+upstreamOrg+"/"+upstreamProviderName+"/releases/tag/v"+version+"\n"+upgradeIssueBodyTemplate,
		"--title="+title,
		"--label="+"kind/enhancement",
	)

	return nil
})

func upgradeIssueExits(ctx context.Context, title, repoOrg, repoName string) (bool, error) {
	// Search through existing pulumiupgradeproviderissue issues to see if we've already created one for this version.
	issues, err := searchIssues(ctx,
		fmt.Sprintf("--repo=%s", repoOrg+"/"+repoName),
		fmt.Sprintf("--search=%q", upgradeIssueToken),
		"--state=open")
	if err != nil {
		return false, err
	}

	// check for exact title match from search results
	for _, issue := range issues {
		if issue.Title == title {
			return true, nil
		}
	}
	return false, nil
}

type issue struct {
	Title  string `json:"title"`
	Number int    `json:"number"`
}

func searchIssues(ctx context.Context, args ...string) ([]issue, error) {
	cmdArgs := []string{"issue", "list", "--json=title,number"}
	cmdArgs = append(cmdArgs, args...)
	issueList := stepv2.Cmd(ctx, "gh", cmdArgs...)
	var issues []issue
	err := json.Unmarshal([]byte(issueList), &issues)
	return issues, err
}
