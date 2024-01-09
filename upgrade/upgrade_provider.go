package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

type CodeMigration = func(ctx context.Context, repo ProviderRepo) (step.Step, error)

var CodeMigrations = map[string]CodeMigration{
	"autoalias":     AddAutoAliasing,
	"assertnoerror": ReplaceAssertNoError,
}

func setEnv(ctx context.Context, k, v string) {
	stepv2.Func20E(k+"="+v, func(ctx context.Context, k, v string) error {
		stepv2.MarkImpure(ctx)
		return os.Setenv(k, v)
	})(ctx, k, v)
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

	err = stepv2.PipelineCtx(ctx, "Set Up Environment", func(ctx context.Context) {
		env := func(k, v string) { setEnv(ctx, k, v) }
		env("GOWORK", "off")
		env("PULUMI_MISSING_DOCS_ERROR", func() string {
			if GetContext(ctx).AllowMissingDocs {
				return "false"
			}
			return "true"
		}())
		env("PULUMI_EXTRA_MAPPING_ERROR", "true")
	})
	if err != nil {
		return err
	}

	err = stepv2.PipelineCtx(ctx, "Discover Provider", func(ctx context.Context) {
		repo.root = OrgProviderRepos(ctx, repoOrg, repoName)
		repo.defaultBranch = pullDefaultBranch(ctx, "origin")
		goMod = getRepoKind(ctx, repo)

		// If we do not have the upstream provider org set in the .upgrade-config.yml, we infer it from the go mod path.
		if GetContext(ctx).UpstreamProviderOrg == "" {
			GetContext(ctx).UpstreamProviderOrg = parseUpstreamProviderOrg(ctx, goMod.Upstream)
		}
		if GetContext(ctx).UpgradeProviderVersion {
			upgradeTarget = planProviderUpgrade(ctx, repoOrg, repoName, goMod, &repo)
		}
	})
	if err != nil {
		return err
	}

	// When we're running a version check, we create the upgrade issue, and then exit.
	if GetContext(ctx).OnlyCheckUpstream {
		// UpgradeProviderVersion may be set to False at this point. We check again.
		if GetContext(ctx).UpgradeProviderVersion {
			pipelineName := fmt.Sprintf("New upstream version detected: v%s", upgradeTarget.Version)
			return stepv2.PipelineCtx(ctx, pipelineName, func(ctx context.Context) {
				createUpstreamUpgradeIssue(ctx,
					repoOrg,
					repoName,
					upgradeTarget.Version.String(),
				)
			})

		}
		fmt.Println(colorize.Bold("No new upstream version detected. Everything up to date."))

		return nil
	}

	err = stepv2.PipelineCtx(ctx, "Plan Upgrade", func(ctx context.Context) {
		if GetContext(ctx).UpgradeBridgeVersion {
			targetBridgeVersion = planBridgeUpgrade(ctx, goMod)
			tfSDKTargetSHA, tfSDKUpgrade = planPluginSDKUpgrade(ctx, repo)
		}

		if GetContext(ctx).UpgradePfVersion {
			targetPfVersion = plantPfUpgrade(ctx, goMod)
		}

		if GetContext(ctx).MajorVersionBump {
			repo.currentVersion = findCurrentMajorVersion(ctx, repoOrg, repoName)
		}

	})
	if err != nil {
		return err
	}

	if GetContext(ctx).UpgradeJavaVersion {
		err = stepv2.PipelineCtx(ctx, "Planning Java Gen Version Update", func(ctx context.Context) {
			if GetContext(ctx).JavaVersion != "" {
				// we are pinning a java gen version via `--java-version`, so we will not query for latest.
				stepv2.Func00("Explicit Java Gen Version", func(ctx context.Context) {
					stepv2.SetLabel(ctx, fmt.Sprintf("Pinning Java Gen Version at %s", GetContext(ctx).JavaVersion))
				})(ctx)
				return
			}

			c := GetContext(ctx)
			c.oldJavaVersion, c.JavaVersion, c.UpgradeJavaVersion = fetchLatestJavaGen(ctx)
		})
		if err != nil {
			return err
		}
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
		!ctx.UpgradeCodeMigration && !ctx.UpgradePfVersion && ctx.TargetPulumiVersion == nil &&
		!ctx.UpgradeJavaVersion {
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
		err := stepv2.PipelineCtx(ctx, "Upgrade Forked Provider", func(ctx context.Context) {
			upgradeUpstreamFork(ctx, repo.name, upgradeTarget.Version, goMod)
		})
		if err != nil {
			return err
		}
	}

	var targetSHA string
	err = stepv2.PipelineCtx(ctx, "Setup working branch", func(ctx context.Context) {
		repo.workingBranch = getWorkingBranch(ctx, *GetContext(ctx), targetBridgeVersion, targetPfVersion, upgradeTarget)
		ensureBranchCheckedOut(ctx, repo.workingBranch)
		repo.prAlreadyExists = hasRemoteBranch(ctx, repo.workingBranch)
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
		steps := []step.Step{}

		// If a provider is patched, running `go mod tidy` without running `make
		// upstream` may be invalid.
		if goMod.Kind.IsPatched() {
			steps = append(steps, step.Cmd("make", "upstream").In(&repo.root))
		}

		steps = append(steps,
			setTFPluginSDKReplace(ctx, repo, &tfSDKTargetSHA),
			step.Cmd("go", "mod", "tidy").In(repo.providerDir()))

		return step.Combined("Update Plugin SDK", steps...)
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
		steps = append(steps, step.Combined("Upgrade Bridge Version",
			step.Cmd("go", "get",
				"github.com/pulumi/pulumi-terraform-bridge/v3@"+targetBridgeVersion.String()),
			step.Cmd("go", "get", "github.com/hashicorp/terraform-plugin-framework"),
			step.Cmd("go", "get", "github.com/hashicorp/terraform-plugin-mux"),
			step.Cmd("go", "mod", "tidy"),
		).In(repo.providerDir()))
	}
	if GetContext(ctx).UpgradePfVersion {
		steps = append(steps, step.Combined("Upgrade Pf Version",
			step.Cmd("go", "get",
				"github.com/pulumi/pulumi-terraform-bridge/pf@"+targetPfVersion.String()),
			step.Cmd("go", "mod", "tidy"),
		).In(repo.providerDir()))
	}

	if ref := GetContext(ctx).TargetPulumiVersion; ref != nil {
		r := func(kind string) string {
			mod := "github.com/pulumi/pulumi/" + kind + "/v3"
			return fmt.Sprintf("%[1]s=%[1]s@%s", mod, ref)

		}

		upgrade := func(name string) step.Step {
			return step.Combined(name,
				step.Cmd("go", "mod", "edit",
					"-replace", r("pkg"),
					"-replace", r("sdk")),
				step.Cmd("go", "mod", "tidy"))
		}

		steps = append(steps, step.Combined("Upgrade Pulumi Version",
			upgrade("provider").In(repo.providerDir()),
			upgrade("examples").In(repo.examplesDir()),
			upgrade("sdk").In(repo.sdkDir())))
	}

	if (GetContext(ctx).UpgradeBridgeVersion || GetContext(ctx).UpgradePfVersion) &&
		GetContext(ctx).TargetPulumiVersion == nil {
		// Having changed the version of pulumi/{sdk,pkg} that we are using, we
		// need to propagate that change to the go.mod in {sdk,examples}/go.mod
		//
		// We make sure that TargetPulumiVersion == "", since we cannot discover
		// the version of a replace statement.
		steps = append(steps, applyPulumiVersion(ctx, repo))
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

	ok := step.Run(ctx, step.Combined("Update Repository", steps...))
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
		ctx = stepv2.WithEnv(ctx, &stepv2.SetCwd{To: repo.root})

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
		// Write Java Gen Version file
		if GetContext(ctx).UpgradeJavaVersion {
			stepv2.WriteFile(ctx, ".pulumi-java-gen.version", GetContext(ctx).JavaVersion)
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
