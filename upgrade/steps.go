// A collection of functions that return relevant steps to upgrade a provider
package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	goSemver "golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

// A "git commit" step that is resilient to no changes in the directory.
//
// This is required to accommodate failure and retry in the `git` push steps.
var gitCommit = stepv2.Func10("git commit", func(ctx context.Context, msg string) {
	status := stepv2.Cmd(ctx, "git", "status", "--porcelain=1")
	if len(status) > 0 {
		stepv2.Cmd(ctx, "git", "commit", "-m", msg)
	} else {
		stepv2.SetLabel(ctx, msg+": nothing to commit")
	}
})

// Upgrade the upstream fork of a pulumi provider.
//
// The SHA of the new upstream branch is returned.
var upgradeUpstreamFork = stepv2.Func31("Upgrade Forked Provider", func(ctx context.Context, name string, target *semver.Version, goMod *GoMod) string {
	upstreamPath := ensureUpstreamRepo(ctx, goMod.Fork.Old.Path)

	// Run the rest of the function inside of upstreamPath
	ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: upstreamPath})

	remoteName := strings.TrimPrefix(name, "pulumi-")
	ensurePulumiRemote(ctx, remoteName)

	stepv2.Cmd(ctx, "git", "fetch", "pulumi")
	stepv2.Cmd(ctx, "git", "fetch", "origin", "--tags")

	previousUpstreamVersion := stepv2.Func01E("Discover Previous Upstream Version", func(ctx context.Context) (*semver.Version, error) {
		b := stepv2.Cmd(ctx, "git", "branch", "--remote", "--list", "pulumi/upstream-v*")
		lines := strings.Split(string(b), "\n")
		var previous *semver.Version
		for _, line := range lines {
			line = strings.TrimSpace(line)
			version, err := semver.NewVersion(strings.TrimPrefix(line, "pulumi/upstream-v"))
			if err != nil {
				continue
			}
			if previous == nil || previous.LessThan(version) {
				previous = version
			}
		}
		if previous == nil {
			return nil, fmt.Errorf("no version found")
		}
		return previous, nil
	})(ctx)

	stepv2.Cmd(ctx, "git", "checkout", "pulumi/upstream-v"+previousUpstreamVersion.String())

	stepv2.Func00("Upstream Branch", func(context.Context) {
		target := "upstream-v" + target.String()
		var branchExists bool
		lines := strings.Split(stepv2.Cmd(ctx, "git", "branch"), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == target {
				branchExists = true
				break
			}
		}
		if !branchExists {
			stepv2.SetLabel(ctx, "creating"+target)
			stepv2.Cmd(ctx, "git", "checkout", "-b", target)
			return
		}
		stepv2.SetLabel(ctx, target+" already exists")
	})(ctx)

	stepv2.Cmd(ctx, "git", "merge", "v"+target.String())
	stepv2.Cmd(ctx, "go", "build", ".")
	stepv2.Cmd(ctx, "git", "push", "pulumi", "upstream-v"+target.String())

	return stepv2.Func01("Get Head Commit", func(context.Context) string {
		c := strings.TrimSpace(stepv2.Cmd(ctx, "git", "rev-parse", "HEAD"))
		stepv2.SetLabel(ctx, c)
		return c
	})(ctx)
})

// Ensure that the upstream repo exists.
//
// The path that the upstream repo exists at is returned.
var ensureUpstreamRepo = stepv2.Func11("Ensure Upstream Repo", func(ctx context.Context, repoPath string) string {
	expectedLocation := stepv2.Func11E("Expected Location",
		func(ctx context.Context, repoPath string) (string, error) {
			cwd := stepv2.GetCwd(ctx)
			loc, err := getRepoExpectedLocation(ctx, cwd, repoPath)
			if err != nil {
				return "", err
			}
			stepv2.SetLabel(ctx, loc)
			return loc, nil
		})(ctx, repoPath)

	repoExists := stepv2.Func11E("Repo Exists", func(ctx context.Context, loc string) (bool, error) {
		info, exists := stepv2.Stat(ctx, loc)
		if !exists {
			return false, nil
		}
		if !info.IsDir {
			return false, fmt.Errorf("'%s' not a directory", loc)
		}
		return true, nil
	})(ctx, expectedLocation)

	if !repoExists {
		stepv2.Func10("Downloading", func(ctx context.Context, path string) {
			targetDir := stepv2.NamedValue(ctx, "Target Dir", filepath.Dir(path))
			stepv2.MkDirAll(ctx, targetDir, 0700)
			stepv2.Cmd(ctx, "git", "clone", fmt.Sprintf("https://%s.git", repoPath), path)
		})(ctx, expectedLocation)
	}

	stepv2.Func10("Validate Repository", func(ctx context.Context, path string) {
		ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: expectedLocation})
		stepv2.Cmd(ctx, "git", "status", "--short")
	})(ctx, expectedLocation)

	return expectedLocation
})

func UpgradeProviderVersion(
	ctx context.Context, goMod *GoMod, target *semver.Version,
	repo ProviderRepo, targetSHA, forkedProviderUpstreamCommit string,
) step.Step {
	steps := []step.Step{}
	if goMod.Kind.IsPatched() {
		// If the provider is patched, we don't use the go module system at all. Instead
		// we update the module referenced to the new tag.
		upstreamDir := filepath.Join(repo.root, "upstream")
		steps = append(steps, step.Combined("update patched provider",
			step.Cmd("git", "submodule", "update", "--force", "--init").In(&upstreamDir),
			step.Cmd("git", "fetch", "--tags").In(&upstreamDir),
			// We need to remove any patches to so we can cleanly pull the next upstream version.
			step.Cmd("git", "reset", "HEAD", "--hard").In(&upstreamDir),
			step.Cmd("git", "checkout", "tags/v"+target.String()).In(&upstreamDir),
			step.Cmd("git", "add", "upstream").In(&repo.root),
			// We re-apply changes, eagerly.
			//
			// Failure to perform this step can lead to failures later, for
			// example, we might have a patched in shim dir that is not yet
			// restored, causing `go mod tidy` to fail, even where `make
			// provider` would succeed.
			step.Cmd("make", "upstream").In(&repo.root),
		))
	}

	if !goMod.Kind.IsForked() {
		// We have an upstream we don't control, so we need to get it's SHA. We do this
		// instead of using version tags because we can't ensure that the upstream is
		// versioning their go modules correctly.
		//
		// If they are versioning correctly, `go mod tidy` will resolve the SHA to a tag.
		steps = append(steps,
			step.F("Lookup Tag SHA", func(context.Context) (string, error) {
				upstreamOrg := GetContext(ctx).UpstreamProviderOrg
				upstreamRepo := GetContext(ctx).UpstreamProviderName
				gitHostPath := "https://github.com/" + upstreamOrg + "/" + upstreamRepo

				// special case: we need to use the GitLab url for getting git refs.
				if upstreamOrg == "terraform-provider-gitlab" {
					gitHostPath = "https://gitlab.com/gitlab-org/terraform-provider-gitlab"
				}

				refs, err := gitRefsOf(ctx, gitHostPath, "tags")
				if err != nil {
					return "", err
				}
				if ref, ok := refs.shaOf("refs/tags/v" + target.String()); ok {
					return ref, nil
				}
				return "", fmt.Errorf("could not find SHA for tag '%s'", target.Original())
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
			if prefix, major, ok := module.SplitPathVersion(upstreamPath); ok && major != "" {
				// If we have a version suffix, and we are doing a major
				// version bump, we need to apply the new suffix.
				upstreamPath = fmt.Sprintf("%s/v%d",
					prefix, target.Major())
			}

			return step.Cmd("go", "get", upstreamPath+"@"+targetV)
		}).In(&goModDir))
	}

	if goMod.Kind.IsForked() {
		// If we are running a forked update, we need to replace the reference to the fork
		// with the SHA of the new upstream branch.
		contract.Assertf(forkedProviderUpstreamCommit != "", "fork provider upstream commit cannot be null")

		replaceIn := func(dir *string) {
			steps = append(steps, step.Cmd("go", "mod", "edit", "-replace",
				goMod.Fork.Old.Path+"="+
					goMod.Fork.New.Path+"@"+forkedProviderUpstreamCommit).In(dir))
		}

		replaceIn(&goModDir)
		if goMod.Kind.IsShimmed() {
			replaceIn(repo.providerDir())
		}
	}

	if goMod.Kind.IsShimmed() {
		// When shimmed, we also run `go mod tidy` in the shim directory, and we want to
		// run that before running `go mod tidy` in the main `provider` directory.
		steps = append(steps, step.Cmd("go", "mod", "tidy").In(&goModDir))
	}

	return step.Combined("Update TF Provider", steps...)
}

var maintenanceRelease = stepv2.Func11E("Check if we should release a maintenance patch", func(
	ctx context.Context,
	repo ProviderRepo,
) (bool, error) {
	repoWithOrg := repo.org + "/" + repo.name
	// We ensure a release at least every 8-9 weeks, concurrent with a bridge update.
	// There are 24 * 7 * 8 = 1344 hours in 8 weeks.
	releaseCadence := time.Hour * 24 * 7 * 8

	relInfo, err := latestReleaseInfo(ctx, repoWithOrg)
	if err != nil {
		return false, err
	}

	releaseDate, err := time.Parse(time.RFC3339, relInfo.Latest.PublishedAt)
	if err != nil {
		return false, err
	}

	stepv2.SetLabelf(ctx, "Last provider release date: %s", relInfo.Latest.PublishedAt)
	ago := time.Since(releaseDate).Abs()

	if ago > releaseCadence {
		stepv2.SetLabelf(
			ctx, "Last provider release date: %s. Marking for patch release.", relInfo.Latest.PublishedAt,
		)
		return true, nil
	}
	return false, nil
})

var InformGitHub = stepv2.Func70E("Inform Github", func(
	ctx context.Context, target *UpstreamUpgradeTarget, repo ProviderRepo,
	goMod *GoMod, targetBridgeVersion, targetPfVersion Ref, tfSDKUpgrade string,
	osArgs []string,
) error {
	ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: repo.root})
	c := GetContext(ctx)

	// --force:
	//
	// If there is no existing branch, then --force doesn't have any effect. It is thus safe.
	//
	// If there is an existing branch, then we will want to override it since we don't
	// attempt to build on existing branches.
	stepv2.Cmd(ctx, "git", "push", "--set-upstream", "origin", repo.workingBranch, "--force")

	var prTitle string
	switch {
	case c.UpgradeProviderVersion:
		prTitle = fmt.Sprintf("Upgrade %s to v%s", c.UpstreamProviderName, target.Version)
	case c.UpgradeBridgeVersion:
		prTitle = "Upgrade pulumi-terraform-bridge to " + targetBridgeVersion.String()
	case c.UpgradeCodeMigration:
		prTitle = fmt.Sprintf("Code migration: %s", strings.Join(c.MigrationOpts, ", "))
	case c.UpgradePfVersion:
		prTitle = "Upgrade pulumi-terraform-bridge/pf to " + targetPfVersion.String()
	case c.TargetPulumiVersion != nil:
		prTitle = "Test: Upgrade pulumi/{pkg,sdk} to " + c.TargetPulumiVersion.String()
	case c.UpgradeJavaVersion:
		prTitle = "Upgrade pulumi-java to " + c.JavaVersion
	default:
		return fmt.Errorf("Unknown action")
	}

	prBody := prBody(ctx, repo, target, goMod, targetBridgeVersion, targetPfVersion, tfSDKUpgrade, osArgs)

	if repo.prAlreadyExists {
		// Update the description in case anything else was upgraded (or not
		// upgraded) in this run, compared to the existing PR.
		stepv2.Cmd(ctx, "gh", "pr", "edit", repo.workingBranch,
			"--title", prTitle,
			"--body", prBody)
	} else {
		addLabels := []string{}

		switch {
		// We create release labels when we are running the full pulumi
		// providers process: i.e. when we discovered issues to close at the
		// beginning of the pipeline.
		case c.UpgradeProviderVersion && len(target.GHIssues) > 0:
			label := upgradeLabel(ctx, repo.currentUpstreamVersion, target.Version)
			if label != "" {
				addLabels = []string{"--label", label}
			}
		// On non-upstream upgrades, we will create a patch release label
		// if the provider hasn't been released in 8 weeks.
		case c.MaintenancePatch && !c.UpgradeProviderVersion:
			addLabels = []string{"--label", "needs-release/patch"}
		}

		stepv2.Cmd(ctx, "gh",
			append([]string{"pr", "create",
				"--assignee", c.PrAssign,
				"--base", repo.defaultBranch,
				"--head", repo.workingBranch,
				"--reviewer", c.PrReviewers,
				"--title", prTitle,
				"--body", prBody},
				addLabels...)...)
	}

	// If we are only upgrading the bridge, we won't have a list of issues.
	if !c.UpgradeProviderVersion {
		return nil
	}

	stepv2.Func00("Assign Issues", func(ctx context.Context) {
		// This PR will close issues, so we assign the issues same assignee as the
		// PR itself.
		for _, t := range target.GHIssues {
			stepv2.Cmd(ctx, "gh", "issue", "edit", fmt.Sprintf("%d", t.Number),
				"--add-assignee", c.PrAssign)
		}
	})(ctx)

	return nil
})

var upgradeLabel = stepv2.Func21("Release Label", func(ctx context.Context, from, to *semver.Version) string {
	if to == nil || from == nil {
		return ""
	}

	cmp := func(toF, fromF func() uint64, name string) (string, bool) {
		to, from := toF(), fromF()
		switch {
		case to > from:
			l := "needs-release/" + name
			stepv2.SetLabel(ctx, l)
			return l, true
		case to < from:
			return "", true
		default:
			return "", false
		}
	}

	if l, ok := cmp(to.Major, from.Major, "major"); ok {
		return l
	}
	if l, ok := cmp(to.Minor, from.Minor, "minor"); ok {
		return l
	}
	if l, ok := cmp(to.Patch, from.Patch, "patch"); ok {
		return l
	}
	return ""
})

// Most if not all of our TF SDK based providers use a "replace" based version of
// github.com/hashicorp/terraform-plugin-sdk/v2. To avoid compile errors, we want
// to be using the most up to date version of this plugin.
//
// This is predicated on updating to the latest version being safe. We will need to
// revisit this when a new major version of the plugin SDK is released.
func setTFPluginSDKReplace(ctx context.Context, repo ProviderRepo, targetSHA *string) step.Step {
	// We do discover in a step.Computed so if the fork isn't present, it isn't
	// displayed to the user.
	return step.F("Update TF Plugin SDK Fork", func(context.Context) (string, error) {
		goModFile, err := os.ReadFile("go.mod")
		if err != nil {
			return "", fmt.Errorf("could not find go.mod: %w", err)
		}
		goMod, err := modfile.Parse("go.mod", goModFile, nil)
		if err != nil {
			return "", fmt.Errorf("failed to parse go.mod: %w", err)
		}

		// Otherwise, we need to replace the old version. goMod.AddReplace
		// will handle replacing existing `replace` directives.
		err = goMod.AddReplace("github.com/hashicorp/terraform-plugin-sdk/v2", "",
			"github.com/pulumi/terraform-plugin-sdk/v2", *targetSHA)
		if err != nil {
			return "", fmt.Errorf("failed to update version: %w", err)
		}

		// We now write out the new file over the old file.
		goMod.Cleanup()
		goModFile, err = goMod.Format()
		if err != nil {
			return "", fmt.Errorf("failed to format file as bytes: %w", err)
		}
		err = os.WriteFile("go.mod", goModFile, 0600)
		return "updated", err
	}).In(repo.providerDir())
}

var ensureBranchCheckedOut = stepv2.Func10("Ensure Branch", func(ctx context.Context, branchName string) {
	branches := stepv2.Cmd(ctx, "git", "branch")

	var alreadyExists bool
	var alreadyCurrent bool
	lines := strings.Split(branches, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == branchName {
			alreadyExists = stepv2.NamedValue(ctx, "already exists", true)
			break
		}
		if line == "* "+branchName {
			alreadyCurrent = stepv2.NamedValue(ctx, "already current", true)
			break
		}
	}

	switch {
	case alreadyCurrent:
		// We are already on the right branch, so do nothing
	case alreadyExists:
		stepv2.Cmd(ctx, "git", "checkout", branchName)
	default:
		stepv2.Cmd(ctx, "git", "checkout", "-b", branchName)
	}
})

var hasRemoteBranch = stepv2.Func11("Has Remote Branch", func(ctx context.Context, branchName string) bool {
	prBytes := []byte(stepv2.Cmd(ctx, "gh", "pr", "list", "--json=title,headRefName"))
	prs := []struct {
		Title       string `json:"title"`
		HeadRefName string `json:"headRefName"`
	}{}
	err := json.Unmarshal(prBytes, &prs)
	stepv2.HaltOnError(ctx, err)

	for _, pr := range prs {
		if pr.HeadRefName == branchName {
			stepv2.SetLabel(ctx, fmt.Sprintf("yes %q", pr.Title))
			return true
		}
	}

	stepv2.SetLabel(ctx, "no")
	return false
})

var getWorkingBranch = stepv2.Func41E("Working Branch Name", func(ctx context.Context, c Context,
	targetBridgeVersion, targetPfVersion Ref, upgradeTarget *UpstreamUpgradeTarget) (string, error) {

	ciSuffix := stepv2.Func01("Random Suffix", func(ctx context.Context) string {
		stepv2.MarkImpure(ctx) // This needs to be impure since it is random
		return fmt.Sprintf("-%08d", rand.Intn(int(math.Pow10(8))))
	})

	ret := func(format string, a ...any) (string, error) {
		s := fmt.Sprintf(format, a...)

		if stepv2.GetEnv(ctx, "CI") == "true" {
			s += ciSuffix(ctx)
		}

		stepv2.SetLabel(ctx, s)
		return s, nil
	}

	switch {
	case c.UpgradeProviderVersion:
		return ret("upgrade-%s-to-v%s", c.UpstreamProviderName, upgradeTarget.Version)
	case c.UpgradeBridgeVersion:
		contract.Assertf(targetBridgeVersion != nil,
			"We are upgrading the bridge, so we must have a target version")
		return ret("upgrade-pulumi-terraform-bridge-to-%s", targetBridgeVersion)
	case c.UpgradeCodeMigration:
		return ret("upgrade-code-migration")
	case c.UpgradePfVersion:
		return ret("upgrade-pf-version-to-%s", targetPfVersion)
	case c.TargetPulumiVersion != nil:
		return ret("upgrade-pulumi-version-to-%s", c.TargetPulumiVersion)
	case c.UpgradeJavaVersion:
		return ret("upgrade-java-version-to-%s", c.JavaVersion)
	default:
		return "", fmt.Errorf("calculating branch name: unknown action")
	}
})

func OrgProviderRepos(ctx context.Context, org, repo string) string {
	return ensureUpstreamRepo(ctx, path.Join("github.com", org, repo))
}

var pullDefaultBranch = stepv2.Func11("Pull Default Branch", func(ctx context.Context, remote string) string {
	lsRemoteHeads := stepv2.Cmd(ctx, "git", "ls-remote", "--heads", remote)
	defaultBranch := stepv2.Func01E("Find default Branch", func(ctx context.Context) (string, error) {
		var hasMaster bool
		lines := strings.Split(lsRemoteHeads, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			_, ref, found := strings.Cut(line, "\t")
			contract.Assertf(found, "not found")
			branch := strings.TrimPrefix(ref, "refs/heads/")
			if branch == "master" {
				hasMaster = true
			}
			if branch == "main" {
				stepv2.SetLabel(ctx, branch)
				return branch, nil
			}
		}
		if hasMaster {
			stepv2.SetLabel(ctx, "master")
			return "master", nil
		}
		return "", fmt.Errorf("could not find 'master' or 'main' branch")

	})(ctx)

	stepv2.Cmd(ctx, "git", "fetch")
	stepv2.Cmd(ctx, "git", "checkout", defaultBranch)
	stepv2.Cmd(ctx, "git", "pull", remote)

	return defaultBranch
})

func MajorVersionBump(ctx context.Context, goMod *GoMod, target *UpstreamUpgradeTarget, repo ProviderRepo) step.Step {
	if repo.currentVersion.Major() == 0 {
		// None of these steps are necessary or appropriate when moving from
		// version 0.x to 1.0 because Go modules only require a version suffix for
		// versions >= 2.0.
		return nil
	}

	prev := ""
	if repo.currentVersion.Major() > 1 {
		prev += fmt.Sprintf("/v%d", repo.currentVersion.Major())
	}
	next := fmt.Sprintf("/v%d", repo.currentVersion.IncMajor().Major())

	// Replace s in file, where {} is interpolated into the old and new provider
	// component of the path.
	replaceInFile := func(desc, path, s string) step.Step {
		return UpdateFile(desc+" in "+path, path, func(src []byte) ([]byte, error) {
			old := strings.ReplaceAll(s, "{}", prev)
			new := strings.ReplaceAll(s, "{}", next)
			return bytes.ReplaceAll(src, []byte(old), []byte(new)), nil
		})
	}

	name := filepath.Base(repo.root)
	return step.Combined("Increment Major Version",
		step.F("Next major version", func(context.Context) (string, error) {
			// This step displays the next major version to the user.
			return repo.currentVersion.IncMajor().String(), nil
		}),
		replaceInFile("Update PROVIDER_PATH", "Makefile",
			"PROVIDER_PATH := provider{}",
		).In(&repo.root),
		replaceInFile("Update -X Version", ".goreleaser.yml",
			"github.com/pulumi/"+name+"/provider{}/pkg",
		).In(&repo.root),
		replaceInFile("Update -X Version", ".goreleaser.prerelease.yml",
			"github.com/pulumi/"+name+"/provider{}/pkg",
		).In(&repo.root),
		replaceInFile("Update Go Module (provider)", "go.mod",
			"module github.com/pulumi/"+name+"/provider{}",
		).In(repo.providerDir()),
		replaceInFile("Update Go Module (sdk)", "go.mod",
			"module github.com/pulumi/"+name+"/sdk{}",
		).In(repo.sdkDir()),
		step.F("Update Go Imports", func(context.Context) (string, error) {
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
					[]byte("github.com/pulumi/"+name+"/provider"+prev),
					[]byte("github.com/pulumi/"+name+"/provider"+next),
				)

				if !goMod.Kind.IsPatched() && !goMod.Kind.IsForked() {
					if prefix, major, ok := module.SplitPathVersion(
						goMod.Upstream.Path); ok && major != "" {
						newUpstream := fmt.Sprintf("%s/v%d",
							prefix, target.Version.Major())
						new = bytes.ReplaceAll(data,
							[]byte(goMod.Upstream.Path),
							[]byte(newUpstream))
					}
				}

				if !reflect.DeepEqual(data, new) {
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
		step.F("info.TFProviderModuleVersion", func(context.Context) (string, error) {
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
		contract.Assertf(doc.Kind == yaml.DocumentNode, "must be yaml format")

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
		steps = append(steps, step.F(f, func(context.Context) (string, error) {
			path := filepath.Join(".github", "workflows", f)
			err := addPrefix(path)
			if os.IsNotExist(err) && f != "run-acceptance-tests.yml" {
				return path + " does not exist", nil
			}
			return "", err
		}))
	}
	return step.Combined("VERSION_PREFIX workflows", steps...)
}

func UpdateFile(desc, path string, f func([]byte) ([]byte, error)) step.Step {
	var b bool
	return UpdateFileWithSignal(desc, path, &b, f)
}

func UpdateFileWithSignal(desc, path string, didChange *bool, f func([]byte) ([]byte, error)) step.Step {
	return step.F(desc, func(context.Context) (string, error) {
		stats, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		newBytes, err := f(bytes)
		if err != nil {
			return "", err
		}
		if reflect.DeepEqual(newBytes, bytes) {
			*didChange = false
			return "no change", nil
		}
		*didChange = true
		return "", os.WriteFile(path, newBytes, stats.Mode().Perm())
	})
}

func UpdateFileV2(ctx context.Context, path string, update func(context.Context, string) string) bool {
	return stepv2.Func01("Update "+path, func(ctx context.Context) bool {
		content := stepv2.ReadFile(ctx, path)
		updated := stepv2.Func11("update", update)(ctx, content)
		if content == updated {
			stepv2.SetLabel(ctx, "No change")
			return false
		}

		stepv2.WriteFile(ctx, path, updated)
		return true
	})(ctx)
}

func migrationSteps(ctx context.Context, repo ProviderRepo, providerName string, description string,
	migrationFunc func(resourcesFilePath, providerName string) (bool, error)) ([]step.Step, error) {
	steps := []step.Step{}
	providerName = strings.TrimPrefix(providerName, "pulumi-")
	changesMade := false
	steps = append(steps,
		step.F(description, func(context.Context) (string, error) {
			changes, err := migrationFunc(filepath.Join(*repo.providerDir(), "resources.go"), providerName)
			if err != nil {
				return fmt.Sprintf("failed to perform \"%s\" migration", description), err
			}
			changesMade = changes
			fmt.Println(description, ", changes made: ", changesMade)
			return "", err
		}))
	if changesMade {
		steps = append(steps,
			step.Cmd("gofmt", "-s", "-w", "resources.go").In(repo.providerDir()),
			step.Cmd("git", "add", "resources.go").In(&repo.root),
			step.Cmd("git", "commit", "-m", description).In(&repo.root),
		)
	}

	return steps, nil
}

func AddAutoAliasing(ctx context.Context, repo ProviderRepo) (step.Step, error) {
	providerName := strings.TrimPrefix(repo.name, "pulumi-")
	metadataPath := fmt.Sprintf("%s/cmd/pulumi-resource-%s/bridge-metadata.json", *repo.providerDir(), providerName)
	steps := []step.Step{
		step.F("ensure bridge-metadata.json", func(context.Context) (string, error) {
			if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
				_, err = os.Create(metadataPath)
				if err != nil {
					return "", fmt.Errorf("Could not initialize %s: %w", metadataPath, err)
				}
				return "created", nil
			}
			return "", nil
		}),
		step.Cmd("git", "add", metadataPath).In(&repo.root),
	}
	migrationSteps, err := migrationSteps(ctx, repo, providerName, "Perform auto aliasing migration",
		AutoAliasingMigration)
	if err != nil {
		return nil, err
	}
	steps = append(steps, migrationSteps...)
	return step.Combined("Add AutoAliasing", steps...), nil
}

func ReplaceAssertNoError(ctx context.Context, repo ProviderRepo) (step.Step, error) {
	steps, err := migrationSteps(ctx, repo, repo.name, "Remove deprecated contract.Assert",
		AssertNoErrorMigration)
	if err != nil {
		return nil, err
	}
	return step.Combined("Replace AssertNoError with AssertNoErrorf", steps...), nil
}

// applyPulumiVersion reads the current Pulumi SDK version from provider/go.mod and applies it to:
// sdk/go.mod
// examples/go.mod - we also infer the `pkg` version here and add it.
func applyPulumiVersion(ctx context.Context, repo ProviderRepo) step.Step {
	// When we've updated the bridge version, we need to update the corresponding pulumi version in sdk/go.mod.
	// It needs to match the version used in provider/go.mod, which is *not* necessarily `latest`.
	var newSdkVersion string
	getNewPulumiVersionStep := step.F("Get Pulumi SDK version", func(context.Context) (string, error) {
		modFile := filepath.Join(repo.root, "provider", "go.mod")
		lookupModule := "github.com/pulumi/pulumi/sdk/v3"
		pulumiMod, found, err := currentGoVersionOf(modFile, lookupModule)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("%s: %s not found\n", modFile, lookupModule)
		}
		return pulumiMod.Version, nil
	}).AssignTo(&newSdkVersion)

	goGet := func(pack string) step.Step {
		return step.Computed(func() step.Step {
			return step.Cmd("go", "get",
				"github.com/pulumi/pulumi/"+pack+"/v3@"+newSdkVersion)
		})
	}

	return step.Combined("Upgrade Pulumi version in all places",
		getNewPulumiVersionStep,
		goGet("sdk").In(repo.sdkDir()),
		goGet("sdk").In(repo.examplesDir()),
		goGet("pkg").In(repo.examplesDir()))
}

// Plan the update for a provider.
//
// That means figuring out the old and the new version, and producing a
// UpstreamUpgradeTarget.
var planProviderUpgrade = stepv2.Func41E("Plan Provider Upgrade", func(ctx context.Context,
	repoOrg, repoName string, goMod *GoMod, repo *ProviderRepo) (*UpstreamUpgradeTarget, error) {
	upgradeTarget := getExpectedTarget(ctx, repoOrg+"/"+repoName)
	if upgradeTarget == nil {
		return nil, fmt.Errorf("could not determine an upstream version")
	}
	// If we don't have any upgrades to target, assume that we don't need to upgrade.
	if upgradeTarget.Version == nil {
		GetContext(ctx).UpgradeProviderVersion = false
		GetContext(ctx).MajorVersionBump = false
		stepv2.SetLabel(ctx, "Up to date")
		return nil, nil
	}

	switch {
	case goMod.Kind.IsPatched():
		setCurrentUpstreamFromPatched(ctx, repo)
	case goMod.Kind.IsForked():
		setCurrentUpstreamFromForked(ctx, repo, goMod)
	case goMod.Kind.IsShimmed():
		setCurrentUpstreamFromShimmed(ctx, repo, goMod)
	case goMod.Kind == Plain:
		setCurrentUpstreamFromPlain(ctx, repo, goMod)
	default:
		return nil, fmt.Errorf("Unexpected repo kind: %s", goMod.Kind)
	}

	// If we have a target version, we need to make sure that
	// it is valid for an upgrade.
	var msg string
	if repo.currentUpstreamVersion != nil {
		switch goSemver.Compare("v"+repo.currentUpstreamVersion.String(),
			"v"+upgradeTarget.Version.String()) {

		// Target version is less then the current version
		case 1:
			// This is a weird situation, so we warn
			msg = colorize.Warnf(
				" no upgrade: %s (current) > %s (target)",
				repo.currentUpstreamVersion,
				upgradeTarget.Version)
			GetContext(ctx).UpgradeProviderVersion = false
			GetContext(ctx).MajorVersionBump = false

		// Target version is equal to the current version
		case 0:
			GetContext(ctx).UpgradeProviderVersion = false
			GetContext(ctx).MajorVersionBump = false
			msg = "Up to date"

		// Target version is greater than the current version, so upgrade
		case -1:
			msg = fmt.Sprintf("%s -> %s",
				repo.currentUpstreamVersion,
				upgradeTarget.Version)
		}
	} else {
		// If we don't have an old version, just assume
		// that we will upgrade.
		msg = upgradeTarget.Version.String()
	}

	stepv2.SetLabel(ctx, msg)
	return upgradeTarget, nil
})

var planBridgeUpgrade = stepv2.Func11E("Planning Bridge Upgrade", func(
	ctx context.Context, goMod *GoMod,
) (Ref, error) {
	found := func(r Ref) (Ref, error) {
		stepv2.SetLabelf(ctx, "%s -> %v", goMod.Bridge.Version, r)
		return r, nil
	}
	switch v := GetContext(ctx).TargetBridgeRef.(type) {
	case nil:
		return nil, fmt.Errorf("--target-bridge-version is required here")
	case *HashReference:
		return found(v)
	case *Version:
		// If our target upgrade version is the same as our
		// current version, we skip the update.
		if v.String() == goMod.Bridge.Version {
			GetContext(ctx).UpgradeBridgeVersion = false
			stepv2.SetLabelf(ctx, "Up to date at %s", v.String())
			return nil, nil
		}

		return found(v)
	case *Latest:
		refs := gitRefsOfV2(ctx, "https://github.com/pulumi/pulumi-terraform-bridge.git", "tags")
		latest := latestSemverTag("", refs)
		// If our target upgrade version is the same as our
		// current version, we skip the update.
		if latest.Original() == goMod.Bridge.Version {
			GetContext(ctx).UpgradeBridgeVersion = false
			stepv2.SetLabelf(ctx, "Up to date at %s", latest.Original())
			return nil, nil
		}
		return found(&Version{latest})
	default:
		panic(fmt.Sprintf("Unknown type of ref: %s (%[1]T)", v))
	}
})

var planPluginSDKUpgrade = stepv2.Func12E("Planning Plugin SDK Upgrade", func(
	ctx context.Context, repo ProviderRepo,
) (_, display string, _ error) {
	defer func() { stepv2.SetLabel(ctx, display) }()
	current, ok := originalGoVersionOfV2(ctx, repo, "provider/go.mod",
		"github.com/pulumi/terraform-plugin-sdk/v2")
	if !ok {
		return "", "not found", nil
	}
	refs := gitRefsOfV2(ctx,
		"https://github.com/pulumi/terraform-plugin-sdk.git", "heads")
	currentRef, err := module.PseudoVersionRev(current.Version)
	if err != nil {
		return "", "", fmt.Errorf("unable to parse PseudoVersionRef %q: %w",
			current.Version, err)
	}
	latest := latestSemverTag("upstream-", refs)
	currentBranch, ok := refs.labelOf(currentRef)
	if !ok {
		// use latest versioned branch
		return "", fmt.Sprintf("Could not find head branch at ref %s. Upgrading to "+
			"latest branch at %s instead.", currentRef, latest), nil
	}

	trim := func(branch string) string {
		const p = "refs/heads/upstream-"
		return strings.TrimPrefix(branch, p)
	}
	currentBranch = trim(currentBranch)

	// We are guaranteed to get a non-nil result because there
	// are semver tags released tags with this prefix.
	if latest.Original() == currentBranch {
		return "", fmt.Sprintf("Up to date at %s", latest), nil
	}
	latestTag := fmt.Sprintf("refs/heads/upstream-%s", latest.Original())
	latestSha, ok := refs.shaOf(latestTag)
	contract.Assertf(ok, "Failed to lookup sha of known tag: %q not in %#v",
		latestTag, refs.labelToRef)

	return latestSha, fmt.Sprintf("%s -> %s", currentBranch, latest), nil
})

var plantPfUpgrade = stepv2.Func11E("Planning Plugin Framework Upgrade", func(
	ctx context.Context, goMod *GoMod,
) (Ref, error) {
	found := func(r Ref) (Ref, error) {
		stepv2.SetLabelf(ctx, "%s -> %s", goMod.Pf.Version, r)
		return r, nil
	}
	if goMod.Pf.Version == "" {
		// PF is not used on this provider, so we disable
		// the upgrade attempt and move on.
		GetContext(ctx).UpgradePfVersion = false
		stepv2.SetLabel(ctx, "Unused")
		return nil, nil
	}
	switch GetContext(ctx).TargetBridgeRef.(type) {
	case *HashReference:
		// if --target-bridge-version has specified a hash
		// reference, use that reference for pf code as well
		return found(GetContext(ctx).TargetBridgeRef)
	default:
		// in all other cases, compute the latest pf tag
		refs := gitRefsOfV2(ctx, "https://"+modPathWithoutVersion(goMod.Bridge.Path),
			"tags")
		targetVersion := latestSemverTag("pf/", refs)

		// If our target upgrade version is the same as our current version, we skip the update.
		if targetVersion.Original() == goMod.Pf.Version {
			GetContext(ctx).UpgradePfVersion = false
			stepv2.SetLabelf(ctx, "Up to date at %s", targetVersion)
			return nil, nil
		}

		return found(&Version{targetVersion})
	}
})

var fetchLatestJavaGen = stepv2.Func03("Fetching latest Java Gen", func(ctx context.Context) (string, string, bool) {
	latestJavaGen := latestReleaseVersion(ctx, "pulumi/pulumi-java")
	var currentJavaGen string
	_, exists := stepv2.Stat(ctx, ".pulumi-java-gen.version")
	if !exists {
		// use dummy placeholder in lieu of reading from file
		currentJavaGen = "0.0.0"
	} else {
		currentJavaGen = stepv2.ReadFile(ctx, ".pulumi-java-gen.version")
	}
	// we do not upgrade Java if the two versions are the same
	if latestJavaGen.String() == currentJavaGen {
		GetContext(ctx).UpgradeJavaVersion = false
		stepv2.Func00("Up to date at", func(ctx context.Context) {
			stepv2.SetLabel(ctx, latestJavaGen.String())
		})(ctx)
		return "", "", false
	}
	// Set latest Java Gen version in the context
	GetContext(ctx).JavaVersion = latestJavaGen.String()
	// Also set oldJavaVersion so we can report later when opening the PR
	GetContext(ctx).oldJavaVersion = currentJavaGen
	stepv2.Func00("Upgrading Java Gen Version", func(ctx context.Context) {
		upgrades := fmt.Sprintf("%s -> %s", currentJavaGen, latestJavaGen)
		stepv2.SetLabel(ctx, upgrades)
	})(ctx)
	stepv2.SetLabel(ctx, latestJavaGen.String())
	return currentJavaGen, latestJavaGen.String(), true
})

var parseUpstreamProviderOrg = stepv2.Func11("Get UpstreamOrg from module version", func(ctx context.Context, upstreamMod module.Version) string {
	// We expect tokens to be of the form:
	//
	//	github.com/${org}/${repo}/${path}
	//
	// The second chunk is the org name.
	tok := strings.Split(modPathWithoutVersion(upstreamMod.Path), "/")
	return tok[1]
})
