package upgrade

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"golang.org/x/mod/semver"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
)

type CodeMigration = func(ctx Context, repo ProviderRepo) (step.Step, error)

var CodeMigrations = map[string]CodeMigration{
	"autoalias":     AddAutoAliasing,
	"assertnoerror": ReplaceAssertNoError,
}

func UpgradeProvider(ctx Context, repoOrg, repoName string) error {
	var err error
	repo := ProviderRepo{
		name: repoName,
		org:  repoOrg,
	}
	var targetBridgeVersion string
	var upgradeTarget *UpstreamUpgradeTarget
	var goMod *GoMod

	ok := step.Run(step.Combined("Setting Up Environment",
		step.Env("GOWORK", "off"),
		step.Env("PULUMI_MISSING_DOCS_ERROR", "true"),
		step.Env("PULUMI_CONVERT_EXAMPLES_CACHE_DIR", ""),
	))
	if !ok {
		return ErrHandled
	}

	discoverSteps := []step.Step{
		OrgProviderRepos(ctx, repoOrg, repoName).AssignTo(&repo.root),
		PullDefaultBranch(ctx, "origin").In(&repo.root).
			AssignTo(&repo.defaultBranch),
	}

	discoverSteps = append(discoverSteps, step.F("Repo kind", func() (string, error) {
		goMod, err = GetRepoKind(ctx, repo)
		if err != nil {
			return "", err
		}
		return string(goMod.Kind), nil
	}))

	if ctx.UpgradeProviderVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Provider Update", func() (string, error) {
				var msg string
				upgradeTarget, msg, err = GetExpectedTarget(ctx, repoOrg+"/"+repoName,
					goMod.UpstreamProviderOrg)
				if err != nil {
					return "", err
				}
				if upgradeTarget == nil {
					return "", errors.New("could not determine an upstream version")
				}

				// If we have upgrades to perform, we list the new version we will target
				if upgradeTarget.Version == nil {
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
					if semver.Compare("v"+repo.currentUpstreamVersion.String(),
						"v"+upgradeTarget.Version.String()) != -1 {
						return "", fmt.Errorf("current upstream version %v is greater than/ equal to the target version %v",
							repo.currentUpstreamVersion, upgradeTarget.Version)
					}
					previous = fmt.Sprintf("%s -> ", repo.currentUpstreamVersion)
				}

				return previous + upgradeTarget.Version.String() + msg, nil
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
				repo.currentVersion, err = latestRelease(ctx, repoOrg+"/"+repoName)
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
		shouldMajorVersionBump := repo.currentUpstreamVersion.Major() != upgradeTarget.Version.Major()
		if ctx.MajorVersionBump && !shouldMajorVersionBump {
			return fmt.Errorf("--major version update indicated, but no major upgrade available (already on v%d)",
				repo.currentUpstreamVersion.Major())
		} else if !ctx.MajorVersionBump && shouldMajorVersionBump {
			return fmt.Errorf("This is a major version update (v%d -> v%d), but --major was not passed",
				repo.currentUpstreamVersion.Major(), upgradeTarget.Version.Major())
		}
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion &&
		!ctx.UpgradeCodeMigration && ctx.UpgradeSdkVersion {
		fmt.Println(colorize.Bold("No actions needed"))
		return nil
	}

	if ctx.UpgradeCodeMigration && len(ctx.MigrationOpts) == 0 {
		keys := make([]string, 0, len(CodeMigrations))
		for k := range CodeMigrations {
			keys = append(keys, k)
		}
		ctx.MigrationOpts = keys
	} else if !ctx.UpgradeCodeMigration && len(ctx.MigrationOpts) > 0 {
		fmt.Println(colorize.Warn("--migration-opts passed but --kind does not indicate a code migration"))
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() && ctx.UpgradeProviderVersion {
		ok = step.Run(upgradeUpstreamFork(ctx, repo.name, upgradeTarget.Version, goMod).
			AssignTo(&forkedProviderUpstreamCommit))
		if !ok {
			return ErrHandled
		}
	}

	var targetSHA string
	if ctx.UpgradeProviderVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-%s-to-v%s",
			ctx.UpstreamProviderName, upgradeTarget.Version)
	} else if ctx.UpgradeBridgeVersion {
		contract.Assertf(targetBridgeVersion != "",
			"We are upgrading the bridge, so we must have a target version")
		repo.workingBranch = fmt.Sprintf("upgrade-pulumi-terraform-bridge-to-%s",
			targetBridgeVersion)
	} else if ctx.UpgradeCodeMigration {
		repo.workingBranch = "upgrade-code-migration"
	} else {
		return fmt.Errorf("calculating branch name: unknown action")
	}
	steps := []step.Step{
		EnsureBranchCheckedOut(ctx, repo.workingBranch).In(&repo.root),
	}

	if ctx.MajorVersionBump {
		steps = append(steps, MajorVersionBump(ctx, goMod, upgradeTarget, repo))

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
		steps = append(steps, UpgradeProviderVersion(ctx, goMod, upgradeTarget.Version, repo,
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
	if ctx.UpgradeSdkVersion {
		steps = append(steps, step.Combined("Upgrade Pulumi SDK",
			step.Cmd(exec.CommandContext(ctx,
				"go", "get", "github.com/pulumi/pulumi/sdk/v3")).
				In(repo.providerDir()),
			step.Cmd(exec.CommandContext(ctx,
				"go", "get", "github.com/pulumi/pulumi/pkg/v3")).
				In(repo.providerDir())))
	}

	if ctx.UpgradeCodeMigration {
		applied := make(map[string]struct{})
		sort.Slice(ctx.MigrationOpts, func(i, j int) bool {
			return ctx.MigrationOpts[i] < ctx.MigrationOpts[j]
		})
		for _, opt := range ctx.MigrationOpts {
			if _, ok := applied[opt]; ok {
				fmt.Println(colorize.Warn("Duplicate code migration " + colorize.Bold(opt)))
				continue
			}
			applied[opt] = struct{}{}

			getMigration, found := CodeMigrations[opt]
			if !found {
				return fmt.Errorf("unknown migration '%s'", opt)
			}

			migration, err := getMigration(ctx, repo)
			if err != nil {
				return fmt.Errorf("unable implement migration '%s': %w", opt, err)
			}

			steps = append(steps, migration)

		}
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
				base := "module github.com/" + repoName + "/sdk"
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
		InformGitHub(ctx, upgradeTarget, repo, goMod, targetBridgeVersion),
	)

	ok = step.Run(step.Combined("Update Artifacts", artifacts...))
	if !ok {
		return ErrHandled
	}

	return nil
}
