package upgrade

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
)

func UpgradeProvider(ctx Context, name string) error {
	var err error
	var repo ProviderRepo
	var targetBridgeVersion string
	var upgradeTargets UpstreamVersions
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
		PulumiProviderRepos(ctx, name).AssignTo(&repo.root),
		PullDefaultBranch(ctx, "origin").In(&repo.root).
			AssignTo(&repo.defaultBranch),
	}

	discoverSteps = append(discoverSteps, step.F("Repo kind", func() (string, error) {
		goMod, err = GetRepoKind(ctx, repo, upstreamProviderName)
		if err != nil {
			return "", err
		}
		return string(goMod.Kind), nil
	}))

	if ctx.UpgradeProviderVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Provider Update", func() (string, error) {
				var msg string
				upgradeTargets, msg, err = GetExpectedTarget(ctx, name)
				if err != nil {
					return "", err
				}

				// If we have upgrades to perform, we list the new version we will target
				if len(upgradeTargets) == 0 {
					// Otherwise, we don't bother to try to upgrade the provider.
					ctx.UpgradeProviderVersion = false
					ctx.MajorVersionBump = false
					return "Up to date" + msg, nil
				}

				switch {
				case goMod.Kind.IsPatched():
					err = setCurrentUpstreamFromPatched(ctx, &repo)
				case goMod.Kind.IsForked():
					err = setCurrentUpstreamFromForked(ctx, &repo, goMod)
				case goMod.Kind.IsShimmed():
					err = setCurrentUpstreamFromShimmed(ctx, &repo, goMod)
				case goMod.Kind == Plain:
					err = setCurrentUpstreamFromPlain(ctx, &repo, goMod)
				default:
					return "", fmt.Errorf("Unexpected repo kind: %s", goMod.Kind)
				}
				if err != nil {
					return "", fmt.Errorf("current upstream version: %w", err)
				}

				var previous string
				if repo.currentUpstreamVersion != nil {
					previous = fmt.Sprintf("%s -> ", repo.currentUpstreamVersion)
				}

				return previous + upgradeTargets.Latest().String() + msg, nil
			}))
	}

	if ctx.UpgradeBridgeVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Bridge Update", func() (string, error) {
				latest, err := latestRelease(ctx, "pulumi/pulumi-terraform-bridge")
				if err != nil {
					return "", err
				}

				// If our target upgrade version is the same as our current version, we skip the update.
				if latest.Original() == goMod.Bridge.Version {
					ctx.UpgradeBridgeVersion = false
					return fmt.Sprintf("Up to date at %s", latest.Original()), nil
				}

				targetBridgeVersion = latest.Original()
				return fmt.Sprintf("%s -> %s", goMod.Bridge.Version, latest.Original()), nil
			}))
	}

	if ctx.MajorVersionBump {
		discoverSteps = append(discoverSteps,
			step.F("Current Major Version", func() (string, error) {
				var err error
				repo.currentVersion, err = latestRelease(ctx, "pulumi/"+name)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d", repo.currentVersion.Major()), nil
			}))
	}

	ok = step.Run(step.Combined("Discovering Repository", discoverSteps...))
	if !ok {
		return ErrHandled
	}

	if ctx.UpgradeProviderVersion {
		shouldMajorVersionBump := repo.currentUpstreamVersion.Major() != upgradeTargets.Latest().Major()
		if ctx.MajorVersionBump && !shouldMajorVersionBump {
			return fmt.Errorf("--major version update indicated, but no major upgrade available (already on v%d)",
				repo.currentUpstreamVersion.Major())
		} else if !ctx.MajorVersionBump && shouldMajorVersionBump {
			return fmt.Errorf("This is a major version update (v%d -> v%d), but --major was not passed",
				repo.currentUpstreamVersion.Major(), upgradeTargets.Latest().Major())
		}
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion {
		fmt.Println(colorize.Bold("No actions needed"))
		return nil
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() && ctx.UpgradeProviderVersion {
		ok = step.Run(upgradeUpstreamFork(ctx, name, upgradeTargets.Latest(), goMod).
			AssignTo(&forkedProviderUpstreamCommit))
		if !ok {
			return ErrHandled
		}
	}

	var targetSHA string
	if ctx.UpgradeProviderVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-terraform-provider-%s-to-v%s",
			upstreamProviderName, upgradeTargets.Latest())
	} else if ctx.UpgradeBridgeVersion {
		contract.Assertf(targetBridgeVersion != "",
			"We are upgrading the bridge, so we must have a target version")
		repo.workingBranch = fmt.Sprintf("upgrade-pulumi-terraform-bridge-to-%s",
			targetBridgeVersion)
	} else {
		return fmt.Errorf("calculating branch name: unknown action")
	}
	steps := []step.Step{
		EnsureBranchCheckedOut(ctx, repo.workingBranch).In(&repo.root),
	}

	if ctx.MajorVersionBump {
		steps = append(steps, MajorVersionBump(ctx, goMod, upgradeTargets, repo))

		defer func() {
			fmt.Printf("\n\n" + colorize.Warn("Major Version Updates are not fully automated!") + "\n")
			fmt.Printf("Steps 1..9, 12 and 13 have been automated. Step 11 can be skipped.\n")
			fmt.Printf("%s need to complete Step 10: Updating README.md and sdk/python/README.md "+
				"in a follow up commit.\n", colorize.Bold("You"))
			fmt.Printf("Steps are listed at\n\t" +
				"https://github.com/pulumi/platform-providers-team/blob/main/playbooks/tf-provider-major-version-update.md\n")
		}()
	}

	if ctx.UpgradeProviderVersion {
		steps = append(steps, UpgradeProviderVersion(ctx, goMod, upgradeTargets.Latest(), repo,
			targetSHA, forkedProviderUpstreamCommit))
	} else if goMod.Kind.IsPatched() {
		// If we are upgrading the provider version, then the upgrade will leave
		// `upstream` in a usable state. Otherwise, we need to call `make
		// upstream` to ensure that the module is valid (for `go get` and `go mod
		// tidy`.
		steps = append(steps, step.Cmd(exec.CommandContext(ctx, "make", "upstream")).In(&repo.root))
	}

	if ctx.UpgradeBridgeVersion {
		steps = append(steps, step.Cmd(exec.CommandContext(ctx,
			"go", "get", "github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion)).
			In(repo.providerDir()))
	}

	artifacts := append(steps,
		step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(repo.providerDir()),
		step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(repo.examplesDir()),
		step.Cmd(exec.CommandContext(ctx, "pulumi", "plugin", "rm", "--all", "--yes")),
		step.Cmd(exec.CommandContext(ctx, "make", "tfgen")).In(&repo.root),
		step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
		GitCommit(ctx, "make tfgen").In(&repo.root),
		step.Cmd(exec.CommandContext(ctx, "make", "build_sdks")).In(&repo.root),
		step.Computed(func() step.Step {
			if !ctx.MajorVersionBump {
				return nil
			}

			return UpdateFile("Update module in sdk/go.mod", "sdk/go.mod", func(b []byte) ([]byte, error) {
				base := "module github.com/pulumi/" + name + "/sdk"
				old := base
				if repo.currentVersion.Major() > 1 {
					old += fmt.Sprintf("/v%d", repo.currentVersion.Major())
				}
				new := base + fmt.Sprintf("/v%d", repo.currentVersion.Major()+1)
				return bytes.ReplaceAll(b, []byte(old), []byte(new)), nil
			}).In(&repo.root)
		}),
		step.Computed(func() step.Step {
			if !ctx.MajorVersionBump {
				return nil
			}
			dir := filepath.Join(repo.root, "sdk")
			return step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).
				In(&dir)
		}),
		step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&repo.root),
		GitCommit(ctx, "make build_sdks").In(&repo.root),
		InformGitHub(ctx, upgradeTargets, repo, goMod,
			upstreamProviderName, targetBridgeVersion),
	)

	ok = step.Run(step.Combined("Update Artifacts", artifacts...))
	if !ok {
		return ErrHandled
	}

	return nil
}
