// A collection of functions that return relevant steps to upgrade a provider
package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"

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
	if s, ok := ProviderName[remoteName]; ok {
		remoteName = s
	}
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
		info, err := stepv2.Stat(ctx, loc)
		if err != nil {
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

var javaVersionRegexp *regexp.Regexp = regexp.MustCompile(`JAVA_GEN_VERSION := (v[0-9]+\.[0-9]+\.[0-9]+)`)

func UpgradeProviderVersion(
	ctx context.Context, goMod *GoMod, target *semver.Version,
	repo ProviderRepo, targetSHA, forkedProviderUpstreamCommit string,
) step.Step {
	steps := []step.Step{}
	if javaVersion := GetContext(ctx).JavaVersion; javaVersion != "" {
		var didChange bool
		steps = append(steps, step.Combined("Update Java Version",
			step.F("Current Java Version", func(cx context.Context) (string, error) {
				b, err := baseFileAt(cx, repo, "Makefile")
				if err != nil {
					return "", err
				}
				found := javaVersionRegexp.FindSubmatch(b)
				if found == nil {
					return "not found", nil
				}
				oldJavaVersion := string(found[1])
				GetContext(ctx).oldJavaVersion = oldJavaVersion
				return oldJavaVersion, nil
			}),
			UpdateFileWithSignal("Update Makefile", "Makefile", &didChange,
				func(b []byte) ([]byte, error) {
					version := javaVersionRegexp.FindSubmatchIndex(b)
					if version == nil {
						return nil, fmt.Errorf("Java version set: could not find JAVA_GEN_VERSION")
					}
					var out bytes.Buffer
					out.Write(b[:version[2]])
					out.WriteString(javaVersion)
					out.Write(b[version[3]:])
					return out.Bytes(), nil
				}),
			step.When(&didChange,
				step.Cmd("rm", "-f", filepath.Join("bin", "pulumi-java-gen"))),
			step.When(&didChange,
				step.Cmd("rm", "-f", filepath.Join("bin", "pulumi-language-java"))),
		))
	}
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
		// It they are versioning correctly, `go mod tidy` will resolve the SHA to a tag.
		steps = append(steps,
			step.F("Lookup Tag SHA", func(context.Context) (string, error) {
				path, err := getGitHubPath(goMod.Upstream.Path)
				if err != nil {
					return "", err
				}
				refs, err := gitRefsOf(ctx, "https://"+modPathWithoutVersion(path),
					"tags")
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
			if indx := versionSuffix.FindStringIndex(upstreamPath); indx != nil {
				// If we have a version suffix, and we are doing a major
				// version bump, we need to apply the new suffix.
				upstreamPath = fmt.Sprintf("%s/v%d",
					upstreamPath[:indx[0]],
					target.Major())
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
		stepv2.Cmd(ctx, "gh", "pr", "create",
			"--assignee", c.PrAssign,
			"--base", repo.defaultBranch,
			"--head", repo.workingBranch,
			"--reviewer", c.PrReviewers,
			"--title", prTitle,
			"--body", prBody)
	}

	// If we are only upgrading the bridge, we wont have a list of issues.
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
	ret := func(format string, a ...any) (string, error) {
		s := fmt.Sprintf(format, a...)
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
	default:
		return "", fmt.Errorf("calculating branch name: unknown action")
	}
})

func OrgProviderRepos(ctx context.Context, org, repo string) string {
	return ensureUpstreamRepo(ctx, path.Join("github.com", org, repo))
}

func PullDefaultBranch(ctx context.Context, remote string) step.Step {
	var lsRemoteHeads string
	var defaultBranch string
	return step.Combined("pull default branch",
		step.Cmd("git", "ls-remote", "--heads", remote).AssignTo(&lsRemoteHeads),
		step.F("finding default branch", func(context.Context) (string, error) {
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
					return branch, nil
				}
			}
			if hasMaster {
				return "master", nil
			}
			return "", fmt.Errorf("could not find 'master' or 'main' branch")
		}).AssignTo(&defaultBranch),
		step.Cmd("git", "fetch"),
		step.Computed(func() step.Step {
			return step.Cmd("git", "checkout", defaultBranch)
		}),
		step.Cmd("git", "pull", remote),
	).Return(&defaultBranch)
}

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
					if idx := versionSuffix.FindStringIndex(goMod.Upstream.Path); idx != nil {
						newUpstream := fmt.Sprintf("%s/v%d",
							goMod.Upstream.Path[:idx[0]],
							target.Version.Major(),
						)
						new = bytes.ReplaceAll(data,
							[]byte(goMod.Upstream.Path),
							[]byte(newUpstream),
						)
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
