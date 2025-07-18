package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/step"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

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
		Name: repoName,
		Org:  repoOrg,
	}
	var targetBridgeVersion Ref
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
		// If the user set --repo-path as CWD, assume all git content is already in-place; simply infer the main
		// branch without pulling anything. Otherwise, pull.
		if GetContext(ctx).IsCWD() {
			repo.defaultBranch = findDefaultBranch(ctx, "origin")
		} else {
			repo.defaultBranch = pullDefaultBranch(ctx, "origin")
		}
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
			if targetBridgeVersion != nil {
				tbv := targetBridgeVersion.String()
				tfSDKTargetSHA, tfSDKUpgrade = planPluginSDKUpgrade(ctx, tbv)
			}
			// Check if we need to release a maintenance patch and set context if so
			GetContext(ctx).MaintenancePatch = maintenanceRelease(ctx, repo)
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
			return fmt.Errorf("this is a major version update (v%d -> v%d), but --major was not passed",
				repo.currentUpstreamVersion.Major(), upgradeTarget.Version.Major())
		}
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if ctx := GetContext(ctx); !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion &&
		ctx.TargetPulumiVersion == nil && !ctx.UpgradeJavaVersion {
		fmt.Println(colorize.Bold("No actions needed"))
		return nil
	}

	if prTitle, err := prTitle(ctx, upgradeTarget, targetBridgeVersion); err != nil {
		return err
	} else {
		repo.prTitle = prTitle
	}

	prTitlePrefix := GetContext(ctx).PRTitlePrefix

	var targetSHA string
	err = stepv2.PipelineCtx(ctx, "Setup working branch", func(ctx context.Context) {
		repo.workingBranch = getWorkingBranch(ctx, *GetContext(ctx), targetBridgeVersion, upgradeTarget, prTitlePrefix)
		ensureBranchCheckedOut(ctx, repo.workingBranch)
		repo.prAlreadyExists = hasExistingPr(ctx, repo.workingBranch, repo.Org+"/"+repo.Name)
	})
	if err != nil {
		return err
	}

	if GetContext(ctx).MajorVersionBump {
		err := stepv2.PipelineCtx(ctx, "Major Version Bump", func(ctx context.Context) {
			majorVersionBump(ctx, goMod, upgradeTarget, repo)
		})
		if err != nil {
			return err
		}
		defer func() {
			fmt.Printf("\n\n%s\n", colorize.Warn("Major Version Updates are experimental!"))
			fmt.Printf("Updating README.md and sdk/python/README.md " +
				"in a follow up commit.\n")
			fmt.Print("Review the steps from the guide at\n\t" +
				"https://github.com/pulumi/platform-providers-team/blob/main/playbooks/Release%3A%20Major%20Version.md\n")
		}()
	}

	var steps []step.Step

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
			setTFPluginSDKReplace(ctx, repo, tfSDKTargetSHA),
			step.Cmd("go", "mod", "tidy").In(repo.providerDir()))

		return step.Combined("Update Plugin SDK", steps...)
	}))

	if GetContext(ctx).UpgradeProviderVersion {
		steps = append(steps, UpgradeProviderVersion(ctx, goMod, upgradeTarget.Version, repo, targetSHA))
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

	if GetContext(ctx).UpgradeBridgeVersion && GetContext(ctx).TargetPulumiVersion == nil {
		// Having changed the version of pulumi/{sdk,pkg} that we are using, we
		// need to propagate that change to the go.mod in {sdk,examples}/go.mod
		//
		// We make sure that TargetPulumiVersion == "", since we cannot discover
		// the version of a replace statement.
		steps = append(steps, applyPulumiVersion(ctx, repo))
	}

	ok := step.Run(ctx, step.Combined("Update Repository", steps...))
	if !ok {
		return ErrHandled
	}

	return stepv2.PipelineCtx(ctx, "Tfgen & Build SDKs",
		tfgenAndBuildSDKs(repo, repoName, upgradeTarget, goMod,
			targetBridgeVersion, tfSDKUpgrade))
}

func tfgenAndBuildSDKs(
	repo ProviderRepo, repoName string, upgradeTarget *UpstreamUpgradeTarget, goMod *GoMod,
	targetBridgeVersion Ref, tfSDKUpgrade string,
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

		stepv2.Cmd(ctx, "pulumi", "plugin", "rm", "--all", "--yes")
		// Write Java Gen Version file
		if GetContext(ctx).UpgradeJavaVersion {
			stepv2.WriteFile(ctx, ".pulumi-java-gen.version", GetContext(ctx).JavaVersion)
		}

		stepv2.Cmd(ctx, "make", "tfgen")

		stepv2.Cmd(ctx, "git", "add", "--all")
		gitCommit(ctx, "make tfgen")

		gen := "generate_sdks"
		if GetContext(ctx).UpgradeJavaVersionOnly {
			gen = "generate_java"
		}

		stepv2.Cmd(ctx, "make", gen)

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
				updateFile(ctx, "go.mod", update)
				stepv2.Cmd(ctx, "go", "mod", "tidy")
			})
		}

		stepv2.Cmd(ctx, "git", "add", "--all")

		gitCommit(ctx, fmt.Sprintf("make %s", gen))

		InformGitHub(ctx, upgradeTarget, repo, goMod, targetBridgeVersion, tfSDKUpgrade, os.Args)
	}
}
