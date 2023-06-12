// A collection of functions that return relevant steps to upgrade a provider
package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"

	"github.com/pulumi/upgrade-provider/step"
)

// A "git commit" step that is resilient to no changes in the directory.
// ß
// This is required to accommodate failure and retry in the `git` push steps.
func GitCommit(ctx context.Context, msg string) step.Step {
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

func ensureUpstreamRepo(ctx Context, repoPath string) step.Step {
	var expectedLocation string
	var repoExists bool
	return step.Combined("Ensure '"+repoPath+"'",
		step.F("Expected Location", func() (string, error) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("could not resolve cwd: %w", err)
			}
			expectedLocation, err = getRepoExpectedLocation(ctx, cwd, repoPath)
			if err != nil {
				return "", err
			}
			if info, err := os.Stat(expectedLocation); err == nil {
				if !info.IsDir() {
					return "", fmt.Errorf("'%s' not a directory", expectedLocation)
				}
				repoExists = true
			}
			return expectedLocation, nil
		}),
		step.Computed(func() step.Step {
			const tag = "Downloading"
			if repoExists {
				return step.F(tag, func() (string, error) {
					return "skipped - already exists", nil
				})
			}
			targetDir := filepath.Dir(expectedLocation)
			return step.Combined(tag,
				step.F("Ensuring Path", func() (string, error) {
					err := os.MkdirAll(targetDir, 0700)
					if err != nil && !os.IsExist(err) {
						return "", err
					}
					return "", nil
				}),
				step.Cmd(exec.CommandContext(ctx, "git", "clone",
					fmt.Sprintf("https://%s.git", repoPath),
					expectedLocation)),
			)
		}),
		step.F("Validating", func() (string, error) {
			return "done", exec.CommandContext(ctx, "git", "status", "--short").Run()
		}).In(&expectedLocation),
	).Return(&expectedLocation)
}

func UpgradeProviderVersion(
	ctx Context, goMod *GoMod, target *semver.Version,
	repo ProviderRepo, targetSHA, forkedProviderUpstreamCommit string,
) step.Step {
	updateLatestPluginSDK, didUpdate := getLatestTFPluginSDKReplace(ctx, repo)

	// We start by updating the terraform-plugin-sdk because later updates sometimes
	// rely on it.
	steps := []step.Step{
		updateLatestPluginSDK,
		// If we updated the pinned plugin sdk, then we need to run `go mod tidy`
		// to normalize the ref.
		step.Computed(func() step.Step {
			if !(*didUpdate) {
				return nil
			}
			return step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).
				In(repo.providerDir())
		}),
	}

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
						contract.Assertf(len(parts) == 2, "expected git ls-remote to give '\t' separated values, found line '%s'", line)
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
		contract.Assertf(forkedProviderUpstreamCommit != "", "fork provider upstream commit cannot be null")

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

func InformGitHub(
	ctx Context, target *UpstreamUpgradeTarget, repo ProviderRepo,
	goMod *GoMod, targetBridgeVersion string,
) step.Step {
	pushBranch := step.Cmd(exec.CommandContext(ctx, "git", "push", "--set-upstream",
		"origin", repo.workingBranch)).In(&repo.root)

	var prTitle string
	if ctx.UpgradeProviderVersion {
		prTitle = fmt.Sprintf("Upgrade %s to v%s",
			ctx.UpstreamProviderName, target.Version)
	} else if ctx.UpgradeBridgeVersion {
		prTitle = "Upgrade pulumi-terraform-bridge to " + targetBridgeVersion
	} else if ctx.UpgradeCodeMigration {
		prTitle = fmt.Sprintf("Code migration: %s", strings.Join(ctx.MigrationOpts, ", "))
	} else {
		panic("Unknown action")
	}

	reviewerFlag := ""
	if ctx.PrReviewers != "" {
		reviewerFlag = fmt.Sprintf("--reviewer=%s", ctx.PrReviewers)
	}
	createPR := step.Cmd(exec.CommandContext(ctx, "gh", "pr", "create",
		"--assignee", "@me",
		"--base", repo.defaultBranch,
		"--head", repo.workingBranch,
		reviewerFlag,
		"--title", prTitle,
		"--body", prBody(ctx, repo, target, goMod, targetBridgeVersion),
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
			issues := make([]step.Step, len(target.GHIssues))
			for i, t := range target.GHIssues {
				issues[i] = step.Cmd(exec.CommandContext(ctx,
					"gh", "issue", "edit", fmt.Sprintf("%d", t.Number),
					"--add-assignee", "@me")).In(&repo.root)
			}
			return step.Combined("Self Assign Issues", issues...)
		}),
	)
}

// Most if not all of our TF SDK based providers use a "replace" based version of
// github.com/hashicorp/terraform-plugin-sdk/v2. To avoid compile errors, we want
// to be using the most up to date version of this plugin.
//
// This is predicated on updating to the latest version being safe. We will need to
// revisit this when a new major version of the plugin SDK is released.
func getLatestTFPluginSDKReplace(ctx context.Context, repo ProviderRepo) (step.Step, *bool) {
	name := "Update TF Plugin SDK Fork"
	stepFail := func(msg string, a ...any) step.Step {
		return step.F(name, func() (string, error) {
			return "", fmt.Errorf(msg, a...)
		})
	}

	didReplace := new(bool)

	// We do discover in a step.Computed so if the fork isn't present, it isn't
	// displayed to the user.
	return step.Computed(func() step.Step {
		goModFile, err := os.ReadFile("go.mod")
		if err != nil {
			return stepFail("could not fine go.mod: %w", err)
		}
		goMod, err := modfile.Parse("go.mod", goModFile, nil)
		if err != nil {
			return stepFail("could not parse go.mod: %w", err)
		}

		const targetSrc = "github.com/hashicorp/terraform-plugin-sdk/v2"

		var require *modfile.Require
		for _, r := range goMod.Require {
			if r.Mod.Path == targetSrc {
				require = r
			}
		}
		if require == nil {
			return nil
		}

		var replace *modfile.Replace
		for _, r := range goMod.Replace {
			if r.Old.Path == targetSrc {
				replace = r
				break
			}
		}
		if replace == nil {
			return nil
		}

		return step.F(name, func() (string, error) {
			// If the fork is present, we need to figure out the SHA of the
			// latest upstream version to use.
			const hostRepo = "https://github.com/pulumi/terraform-plugin-sdk.git"
			result, err := exec.CommandContext(ctx, "git",
				"ls-remote", "--heads", hostRepo).Output()
			if err != nil {
				return "", fmt.Errorf("could not get branches: %w", err)
			}
			lines := strings.Split(string(result), "\n")
			versions := make([]*semver.Version, len(lines))
			shas := make([]string, len(lines))
			highest := -1
			for i, line := range lines {
				split := strings.Split(strings.TrimSpace(line), "\t")
				if len(split) < 2 {
					continue
				}
				shas[i] = split[0]
				version, hasVersion := strings.CutPrefix(split[1], "refs/heads/upstream-")
				if !hasVersion {
					continue
				}
				if v, err := semver.NewVersion(version); err == nil {
					versions[i] = v
					if highest == -1 || versions[highest].LessThan(v) {
						highest = i
					}
				}
			}
			if highest == -1 {
				return "", fmt.Errorf("no upstream version found")
			}

			// We now compare the pseudo vision and the latest SHA.
			pseudo, err := module.PseudoVersionRev(replace.New.Version)
			if err != nil {
				return "", fmt.Errorf("not using a branch based replace")
			}

			// If the pseudo version matches the latest SHA, we are already up
			// to date. We don't need to do any edits.
			if strings.HasPrefix(shas[highest], pseudo) {
				return "already up to date", nil
			}

			// Otherwise, we need to replace the old version. goMod.AddReplace
			// will handle replacing existing `replace` directives.
			err = goMod.AddReplace(replace.Old.Path, replace.Old.Version,
				replace.New.Path, shas[highest])
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
			*didReplace = true
			return "updated", err
		})
	}).In(repo.providerDir()), didReplace
}

func EnsureBranchCheckedOut(ctx Context, branchName string) step.Step {
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

func OrgProviderRepos(ctx Context, org, repo string) step.Step {
	return ensureUpstreamRepo(ctx, path.Join("github.com", org, repo))
}

func PullDefaultBranch(ctx Context, remote string) step.Step {
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
		step.Cmd(exec.CommandContext(ctx, "git", "fetch")),
		step.Computed(func() step.Step {
			return step.Cmd(exec.CommandContext(ctx, "git", "checkout", defaultBranch))
		}),
		step.Cmd(exec.CommandContext(ctx, "git", "pull", remote)),
	).Return(&defaultBranch)
}

func MajorVersionBump(ctx Context, goMod *GoMod, target *UpstreamUpgradeTarget, repo ProviderRepo) step.Step {
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
		return UpdateFile(desc+" in "+path, path, func(src []byte) ([]byte, error) {
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
							target.Version.Major(),
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
		f := filepath.Join(".github", "workflows", f)
		steps = append(steps, step.F(f, func() (string, error) {
			return "", addPrefix(f)
		}))
	}
	return step.Combined("VERSION_PREFIX workflows", steps...)
}

func UpdateFile(desc, path string, f func([]byte) ([]byte, error)) step.Step {
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

func migrationSteps(ctx Context, repo ProviderRepo, providerName string, description string,
	migrationFunc func(resourcesFilePath, providerName string) (bool, error)) ([]step.Step, error) {
	steps := []step.Step{}
	providerName = strings.TrimPrefix(providerName, "pulumi-")
	changesMade := false
	steps = append(steps,
		step.F(description, func() (string, error) {
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
			step.Cmd(exec.CommandContext(ctx, "gofmt", "-s", "-w", "resources.go")).In(repo.providerDir()),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "resources.go")).In(&repo.root),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", description)).In(&repo.root),
		)
	}

	return steps, nil
}

func AddAutoAliasing(ctx Context, repo ProviderRepo) (step.Step, error) {
	providerName := strings.TrimPrefix(repo.name, "pulumi-")
	metadataPath := fmt.Sprintf("%s/cmd/pulumi-resource-%s/bridge-metadata.json", *repo.providerDir(), providerName)
	steps := []step.Step{
		step.F("ensure bridge-metadata.json", func() (string, error) {
			if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
				_, err = os.Create(metadataPath)
				if err != nil {
					return "", fmt.Errorf("Could not initialize %s: %w", metadataPath, err)
				}
				return "created", nil
			}
			return "", nil
		}),
		step.Cmd(exec.CommandContext(ctx, "git", "add", metadataPath)).In(&repo.root),
	}
	migrationSteps, err := migrationSteps(ctx, repo, providerName, "Perform auto aliasing migration",
		AutoAliasingMigration)
	if err != nil {
		return nil, err
	}
	steps = append(steps, migrationSteps...)
	return step.Combined("Add AutoAliasing", steps...), nil
}

func ReplaceAssertNoError(ctx Context, repo ProviderRepo) (step.Step, error) {
	steps, err := migrationSteps(ctx, repo, repo.name, "Remove deprecated contract.Assert",
		AssertNoErrorMigration)
	if err != nil {
		return nil, err
	}
	return step.Combined("Replace AssertNoError with AssertNoErrorf", steps...), nil
}
