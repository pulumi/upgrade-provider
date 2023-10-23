package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
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

func UpgradeProvider(ctx context.Context, repoOrg, repoName string) (err error) {

	// Setup ctx to enable replay tests with stepv2:
	if file := os.Getenv("PULUMI_REPLAY"); file != "" {
		var write io.Closer
		ctx, write = stepv2.WithRecord(ctx, file)
		defer func() { err = errors.Join(err, write.Close()) }()
	}

	repo := ProviderRepo{
		name: repoName,
		org:  repoOrg,
	}
	var targetBridgeVersion, targetPfVersion Ref
	var tfSDKUpgrade string
	var tfSDKTargetSHA string
	var upgradeTarget *UpstreamUpgradeTarget
	var goMod *GoMod

	ok := step.Run(ctx, step.Combined("Setting Up Environment",
		step.Env("GOWORK", "off"),
		step.Env("PULUMI_MISSING_DOCS_ERROR", func() string {
			if GetContext(ctx).AllowMissingDocs {
				return "false"
			}
			return "true"
		}()),
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
				switch v := GetContext(ctx).TargetBridgeRef.(type) {
				case nil:
					return "", fmt.Errorf("--target-bridge-version is required here")
				case *HashReference:
					targetBridgeVersion = v
				case *Version:
					// If our target upgrade version is the same as our
					// current version, we skip the update.
					if v.String() == goMod.Bridge.Version {
						GetContext(ctx).UpgradeBridgeVersion = false
						return fmt.Sprintf("Up to date at %s", v.String()), nil
					}

					targetBridgeVersion = v
				case *Latest:
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
					targetBridgeVersion = &Version{semver.MustParse(latest.Original())}
				}
				return fmt.Sprintf("%s -> %v", goMod.Bridge.Version, targetBridgeVersion), nil
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
				switch GetContext(ctx).TargetBridgeRef.(type) {
				case *HashReference:
					// if --target-bridge-version has specified a hash
					// reference, use that reference for pf code as well
					targetPfVersion = GetContext(ctx).TargetBridgeRef
				default:
					// in all other cases, compute the latest pf tag
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

					targetPfVersion = &Version{targetVersion}
				}
				return fmt.Sprintf("%s -> %s", goMod.Pf.Version, targetPfVersion), nil
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

	ok = step.Run(ctx, step.Combined("Discovering Repository", discoverSteps...))
	if !ok {
		return ErrHandled
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
		ok = step.Run(ctx, upgradeUpstreamFork(ctx, repo.name, upgradeTarget.Version, goMod).
			AssignTo(&forkedProviderUpstreamCommit))
		if !ok {
			return ErrHandled
		}
	}

	var targetSHA string
	err = stepv2.PipelineCtx(ctx, "Setup working branch", func(ctx context.Context) {
		repo.workingBranch = getWorkingBranch(ctx, *GetContext(ctx), targetBridgeVersion, targetPfVersion, upgradeTarget)
		ensureBranchCheckedOut(ctx, repo.workingBranch)
	})
	if err != nil {
		return err
	}

	var steps []step.Step

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
			"github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion.String()).
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
			"github.com/pulumi/pulumi-terraform-bridge/pf@"+targetPfVersion.String()).
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

	ok = step.Run(ctx, step.Combined("Update Repository", steps...))
	if !ok {
		return ErrHandled
	}

	return stepv2.PipelineCtx(ctx, "Tfgen & Build SDKs",
		tfgenAndBuildSDKs(repo, repoName, upgradeTarget, goMod,
			targetBridgeVersion, targetPfVersion, tfSDKUpgrade))
}

func tfgenAndBuildSDKs(
	repo ProviderRepo, repoName string, upgradeTarget *UpstreamUpgradeTarget, goMod *GoMod,
	targetBridgeVersion, targetPfVersion Ref, tfSDKUpgrade string,
) func(ctx context.Context) {
	return func(ctx context.Context) {
		ctx = stepv2.WithEnv(ctx, &stepv2.Cwd{To: repo.root})

		stepv2.WithCwd(ctx, *repo.providerDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		stepv2.WithCwd(ctx, *repo.examplesDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		stepv2.WithCwd(ctx, *repo.sdkDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		if GetContext(ctx).RemovePlugins {
			stepv2.Cmd(ctx, "pulumi", "plugin", "rm", "--all", "--yes")
		}

		stepv2.Cmd(ctx, "make", "tfgen")

		stepv2.Cmd(ctx, "git", "add", "--all")
		gitCommit(ctx, "make tfgen")

		stepv2.Cmd(ctx, "make", "build_sdks")

		// Update sdk/go.mod's module after rebuilding the go SDK
		if GetContext(ctx).MajorVersionBump {
			update := func(_ context.Context, s string) string {
				base := "module github.com/" + repoName + "/sdk"
				old := base
				if repo.currentVersion.Major() > 1 {
					old += fmt.Sprintf("/v%d", repo.currentVersion.Major())
				}
				new := base + fmt.Sprintf("/v%d", repo.currentVersion.Major()+1)
				return strings.ReplaceAll(s, old, new)
			}

			stepv2.WithCwd(ctx, *repo.sdkDir(), func(ctx context.Context) {
				UpdateFileV2(ctx, "go.mod", update)
				stepv2.Cmd(ctx, "go", "mod", "tidy")
			})
		}

		stepv2.Cmd(ctx, "git", "add", "--all")

		gitCommit(ctx, "make build_sdks")

		InformGitHub(ctx, upgradeTarget, repo, goMod, targetBridgeVersion,
			targetPfVersion, tfSDKUpgrade, os.Args)
	}
}
