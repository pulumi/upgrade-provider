package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"golang.org/x/mod/module"
	goSemver "golang.org/x/mod/semver"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

type CodeMigration = func(ctx context.Context, repo ProviderRepo) (step.Step, error)

var CodeMigrations = map[string]CodeMigration{
	"autoalias":     AddAutoAliasing,
	"assertnoerror": ReplaceAssertNoError,
}

func UpgradeProvider(ctx context.Context, repoOrg, repoName string) error {
	var err error
	repo := ProviderRepo{
		name: repoName,
		org:  repoOrg,
	}
	var targetBridgeVersion, targetPfVersion, tfSDKUpgrade string
	var tfSDKTargetSHA string
	var upgradeTarget *UpstreamUpgradeTarget
	var goMod *GoMod

	missingDocs := "true"
	if GetContext(ctx).AllowMissingDocs {
		missingDocs = "false"
	}

	ctx = stepv2.WithEnv(ctx,
		&stepv2.EnvVar{Key: "GOWORK", Value: "off"},
		&stepv2.EnvVar{Key: "PULUMI_MISSING_DOCS_ERROR", Value: missingDocs},
		&stepv2.EnvVar{Key: "PULUMI_CONVERT_EXAMPLES_CACHE_DIR", Value: ""},
		&stepv2.Silent{},
	)

	discoverSteps := []step.Step{
		PullDefaultBranch(ctx, "origin").In(&repo.root).
			AssignTo(&repo.defaultBranch),
	}

	discoverSteps = append(discoverSteps, step.F("Repo kind", func(context.Context) (string, error) {
		goMod, err = GetRepoKind(ctx, repo)
		if err != nil {
			return "", err
		}
		return string(goMod.Kind), nil
	}))

	if GetContext(ctx).UpgradeProviderVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Provider Update", func(context.Context) (string, error) {
				upgradeTarget, err = GetExpectedTarget(ctx, repoOrg+"/"+repoName,
					goMod.UpstreamProviderOrg)
				if err != nil {
					return "", fmt.Errorf("expected target: %w", err)
				}
				if upgradeTarget == nil {
					return "", errors.New("could not determine an upstream version")
				}

				// If we don't have any upgrades to target, assume that we don't need to upgrade.
				if upgradeTarget.Version == nil {
					// Otherwise, we don't bother to try to upgrade the provider.
					GetContext(ctx).UpgradeProviderVersion = false
					GetContext(ctx).MajorVersionBump = false
					return "Up to date", nil
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

				// If we have a target version, we need to make sure that
				// it is valid for an upgrade.
				var result string
				if repo.currentUpstreamVersion != nil {
					switch goSemver.Compare("v"+repo.currentUpstreamVersion.String(),
						"v"+upgradeTarget.Version.String()) {

					// Target version is less then the current version
					case 1:
						// This is a weird situation, so we warn
						result = colorize.Warnf(
							" no upgrade: %s (current) > %s (target)",
							repo.currentUpstreamVersion,
							upgradeTarget.Version)
						GetContext(ctx).UpgradeProviderVersion = false
						GetContext(ctx).MajorVersionBump = false

					// Target version is equal to the current version
					case 0:
						GetContext(ctx).UpgradeProviderVersion = false
						GetContext(ctx).MajorVersionBump = false
						result = "Up to date"

					// Target version is greater then the current version, so upgrade
					case -1:
						result = fmt.Sprintf("%s -> %s",
							repo.currentUpstreamVersion,
							upgradeTarget.Version)
					}
				} else {
					// If we don't have an old version, just assume
					// that we will upgrade.
					result = upgradeTarget.Version.String()
				}

				return result, nil
			}))
	}

	if GetContext(ctx).UpgradeBridgeVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Bridge Update", func(context.Context) (string, error) {
				refs, err := gitRefsOf(ctx,
					"https://github.com/pulumi/pulumi-terraform-bridge.git", "tags")
				if err != nil {
					return "", err
				}

				latest := latestSemverTag("", refs)

				// If our target upgrade version is the same as our
				// current version, we skip the update.
				if latest.Original() == goMod.Bridge.Version {
					GetContext(ctx).UpgradeBridgeVersion = false
					return fmt.Sprintf("Up to date at %s", latest.Original()), nil
				}

				targetBridgeVersion = latest.Original()
				return fmt.Sprintf("%s -> %s", goMod.Bridge.Version, latest.Original()), nil
			}),
			step.F("Planning Plugin SDK Update", func(context.Context) (string, error) {
				current, ok, err := originalGoVersionOf(ctx, repo, "provider/go.mod",
					"github.com/pulumi/terraform-plugin-sdk/v2")
				if err != nil {
					return "", err
				}
				if !ok {
					return "not found", nil
				}
				refs, err := gitRefsOf(ctx,
					"https://github.com/pulumi/terraform-plugin-sdk.git", "heads")
				if err != nil {
					return "", err
				}
				currentRef, err := module.PseudoVersionRev(current.Version)
				if err != nil {
					return "", fmt.Errorf("unable to parse PseudoVersionRef %q: %w",
						current.Version, err)
				}
				latest := latestSemverTag("upstream-", refs)
				currentBranch, ok := refs.labelOf(currentRef)
				if !ok {
					// use latest versioned branch
					return fmt.Sprintf("Could not find head branch at ref %s. Upgrading to "+
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
					return fmt.Sprintf("Up to date at %s", latest), nil
				}
				latestTag := fmt.Sprintf("refs/heads/upstream-%s", latest.Original())
				latestSha, ok := refs.shaOf(latestTag)
				contract.Assertf(ok, "Failed to lookup sha of known tag: %q not in %#v",
					latestTag, refs.labelToRef)
				tfSDKTargetSHA = latestSha
				return fmt.Sprintf("%s -> %s", currentBranch, latest), nil
			}).AssignTo(&tfSDKUpgrade),
		)
	}
	if GetContext(ctx).UpgradePfVersion {
		discoverSteps = append(discoverSteps,
			step.F("Planning Plugin Framework Update", func(context.Context) (string, error) {
				if goMod.Pf.Version == "" {
					// PF is not used on this provider, so we disable
					// the upgrade attempt and move on.
					GetContext(ctx).UpgradePfVersion = false
					return "Unused", nil
				}
				refs, err := gitRefsOf(ctx, "https://"+modPathWithoutVersion(goMod.Bridge.Path),
					"tags")
				if err != nil {
					return "", err
				}
				targetVersion := latestSemverTag("pf/", refs)

				// If our target upgrade version is the same as our current version, we skip the update.
				if targetVersion.Original() == goMod.Pf.Version {
					GetContext(ctx).UpgradePfVersion = false
					return fmt.Sprintf("Up to date at %s", targetVersion.String()), nil
				}

				targetPfVersion = "v" + targetVersion.String()
				return fmt.Sprintf("%s -> %s", goMod.Pf.Version, targetVersion.String()), nil
			}))
	}

	if GetContext(ctx).MajorVersionBump {
		discoverSteps = append(discoverSteps,
			step.F("Current Major Version", func(context.Context) (string, error) {
				var err error
				repo.currentVersion, err = latestRelease(ctx, repoOrg+"/"+repoName)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d", repo.currentVersion.Major()), nil
			}))
	}

	if pErr := stepv2.PipelineCtx(ctx, "Discovering Repository", func(ctx context.Context) {
		repoPath := path.Join("github.com", repoOrg, repoName)
		repo.root = stepv2.Call11E(ctx, fmt.Sprintf("Ensure '%s'", repoPath), ensureUpstreamRepoV2, repoPath)
		ok := step.Run(ctx, step.Combined("Discovering Repository", discoverSteps...))
		if !ok {
			err = ErrHandled
		}
	}); pErr != nil {
		return pErr
	}
	if err != nil {
		return err
	}

	if GetContext(ctx).UpgradeProviderVersion {
		shouldMajorVersionBump := repo.currentUpstreamVersion.Major() != upgradeTarget.Version.Major()
		if GetContext(ctx).MajorVersionBump && !shouldMajorVersionBump {
			return fmt.Errorf("--major version update indicated, but no major upgrade available (already on v%d)",
				repo.currentUpstreamVersion.Major())
		} else if !GetContext(ctx).MajorVersionBump && shouldMajorVersionBump {
			return fmt.Errorf("This is a major version update (v%d -> v%d), but --major was not passed",
				repo.currentUpstreamVersion.Major(), upgradeTarget.Version.Major())
		}
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if ctx := GetContext(ctx); !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion &&
		!ctx.UpgradeCodeMigration && !ctx.UpgradeSdkVersion && !ctx.UpgradePfVersion {
		fmt.Println(colorize.Bold("No actions needed"))
		return nil
	}

	if GetContext(ctx).UpgradeCodeMigration && len(GetContext(ctx).MigrationOpts) == 0 {
		keys := make([]string, 0, len(CodeMigrations))
		for k := range CodeMigrations {
			keys = append(keys, k)
		}
		GetContext(ctx).MigrationOpts = keys
	} else if !GetContext(ctx).UpgradeCodeMigration && len(GetContext(ctx).MigrationOpts) > 0 {
		fmt.Println(colorize.Warn("--migration-opts passed but --kind does not indicate a code migration"))
	}

	var forkedProviderUpstreamCommit string
	if goMod.Kind.IsForked() && GetContext(ctx).UpgradeProviderVersion {
		if pErr := stepv2.PipelineCtx(ctx, "Upgrade Upstream Fork", func(ctx context.Context) {
			ok := step.Run(ctx, upgradeUpstreamFork(ctx, repo.name, upgradeTarget.Version, goMod).
				AssignTo(&forkedProviderUpstreamCommit))
			if !ok {
				err = ErrHandled
			}
		}); pErr != nil {
			return pErr
		}
		if err != nil {
			return err
		}
	}

	var targetSHA string
	if ctx := GetContext(ctx); ctx.UpgradeProviderVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-%s-to-v%s",
			ctx.UpstreamProviderName, upgradeTarget.Version)
	} else if ctx.UpgradeBridgeVersion {
		contract.Assertf(targetBridgeVersion != "",
			"We are upgrading the bridge, so we must have a target version")
		repo.workingBranch = fmt.Sprintf("upgrade-pulumi-terraform-bridge-to-%s",
			targetBridgeVersion)
	} else if ctx.UpgradeCodeMigration {
		repo.workingBranch = "upgrade-code-migration"
	} else if ctx.UpgradePfVersion {
		repo.workingBranch = fmt.Sprintf("upgrade-pf-version-to-%s", targetPfVersion)
	} else if ctx.UpgradeSdkVersion {
		repo.workingBranch = "upgrade-pulumi-sdk"
	} else {
		return fmt.Errorf("calculating branch name: unknown action")
	}
	steps := []step.Step{
		EnsureBranchCheckedOut(ctx, repo.workingBranch).In(&repo.root),
	}

	if GetContext(ctx).MajorVersionBump {
		steps = append(steps, MajorVersionBump(ctx, goMod, upgradeTarget, repo))

		defer func() {
			fmt.Printf("\n\n" + colorize.Warn("Major Version Updates are not fully automated!") + "\n")
			fmt.Printf("%s need to complete Step 11: Updating README.md and sdk/python/README.md "+
				"in a follow up commit.\n", colorize.Bold("You"))
			fmt.Printf("Steps are listed at\n\t" +
				"https://github.com/pulumi/platform-providers-team/blob/main/playbooks/tf-provider-major-version-update.md\n")
		}()
	}

	steps = append(steps, step.Computed(func() step.Step {
		// No upgrade was planned, so exit
		if tfSDKTargetSHA == "" {
			return nil
		}
		return step.Combined("Update Plugin SDK",
			setTFPluginSDKReplace(ctx, repo, &tfSDKTargetSHA),
			step.Cmd("go", "mod", "tidy").In(repo.providerDir()),
		)
	}))

	if GetContext(ctx).UpgradeProviderVersion {
		steps = append(steps, UpgradeProviderVersion(ctx, goMod, upgradeTarget.Version, repo,
			targetSHA, forkedProviderUpstreamCommit))
	} else if goMod.Kind.IsPatched() {
		// If we are upgrading the provider version, then the upgrade will leave
		// `upstream` in a usable state. Otherwise, we need to call `make
		// upstream` to ensure that the module is valid (for `go get` and `go mod
		// tidy`.
		steps = append(steps, step.Cmd("make", "upstream").In(&repo.root))
	}

	if GetContext(ctx).UpgradeBridgeVersion {
		steps = append(steps, step.Cmd("go", "get",
			"github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion).
			In(repo.providerDir()))

		steps = append(steps, step.Cmd("go", "get",
			"github.com/hashicorp/terraform-plugin-framework").
			In(repo.providerDir()))
		steps = append(steps, step.Cmd("go", "get",
			"github.com/hashicorp/terraform-plugin-mux").
			In(repo.providerDir()))
		steps = append(steps, step.Cmd("go", "mod", "tidy").
			In(repo.providerDir()))

		// Now that we've upgraded the bridge, we want the other places in the repo to use the bridge's
		// Pulumi version.
		upgradePulumiEverywhereStep := BridgePulumiVersions(ctx, repo)

		steps = append(steps, upgradePulumiEverywhereStep)

	}
	if GetContext(ctx).UpgradeSdkVersion {
		steps = append(steps, step.Combined("Upgrade Pulumi SDK",
			step.Cmd("go", "get", "github.com/pulumi/pulumi/sdk/v3").
				In(repo.providerDir()),
			step.Cmd("go", "get", "github.com/pulumi/pulumi/pkg/v3").
				In(repo.providerDir())),
			step.Cmd("go", "get", "github.com/pulumi/pulumi/sdk/v3").
				In(repo.examplesDir()),
			step.Cmd("go", "get", "github.com/pulumi/pulumi/pkg/v3").
				In(repo.examplesDir()))
	}
	if GetContext(ctx).UpgradePfVersion {
		steps = append(steps, step.Cmd("go", "get",
			"github.com/pulumi/pulumi-terraform-bridge/pf@"+targetPfVersion).
			In(repo.providerDir()))
	}

	if GetContext(ctx).UpgradeCodeMigration {
		applied := make(map[string]struct{})
		sort.Slice(GetContext(ctx).MigrationOpts, func(i, j int) bool {
			m := GetContext(ctx).MigrationOpts
			return m[i] < m[j]
		})
		for _, opt := range GetContext(ctx).MigrationOpts {
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
		step.Cmd("go", "mod", "tidy").In(repo.providerDir()),
		step.Cmd("go", "mod", "tidy").In(repo.examplesDir()),
		step.Cmd("go", "mod", "tidy").In(repo.sdkDir()),
		step.Computed(func() step.Step {
			if GetContext(ctx).RemovePlugins {
				return step.Cmd("pulumi", "plugin", "rm", "--all", "--yes")
			}
			return nil
		}),
		step.Cmd("make", "tfgen").In(&repo.root),
		step.Cmd("git", "add", "--all").In(&repo.root),
		GitCommit(ctx, "make tfgen").In(&repo.root),
		step.Cmd("make", "build_sdks").In(&repo.root),
		step.Computed(func() step.Step {
			if !GetContext(ctx).MajorVersionBump {
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
			if !GetContext(ctx).MajorVersionBump {
				return nil
			}
			dir := filepath.Join(repo.root, "sdk")
			return step.Cmd("go", "mod", "tidy").
				In(&dir)
		}),
		step.Cmd("git", "add", "--all").In(&repo.root),
		GitCommit(ctx, "make build_sdks").In(&repo.root),
		InformGitHub(ctx, upgradeTarget, repo, goMod, targetBridgeVersion, targetPfVersion, tfSDKUpgrade),
	)

	pErr := stepv2.PipelineCtx(ctx, "Update Artifacts", func(ctx context.Context) {
		ok := step.Run(ctx, step.Combined("Update Artifacts", artifacts...))
		if !ok {
			err = ErrHandled
		}
	})
	if pErr != nil {
		return pErr
	}

	return err
}
