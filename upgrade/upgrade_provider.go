package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

func applyMajorVersionPolicy(ctx context.Context, repo *ProviderRepo, upgradeTarget *UpstreamUpgradeTarget) error {
	c := GetContext(ctx)
	if !c.UpgradeProviderVersion || upgradeTarget == nil || upgradeTarget.Version == nil {
		return nil
	}
	if repo.currentUpstreamVersion == nil {
		return fmt.Errorf("could not determine current upstream version; cannot determine whether %s is a major version update",
			upgradeTarget.Version)
	}

	shouldMajorVersionBump := repo.currentUpstreamVersion.Major() != upgradeTarget.Version.Major()
	if c.MajorVersionBump && !shouldMajorVersionBump {
		return fmt.Errorf("--major version update indicated, but no major upgrade available (already on v%d)",
			repo.currentUpstreamVersion.Major())
	}
	if shouldMajorVersionBump {
		if c.AllowMajorVersionBump {
			c.MajorVersionBump = true
			return nil
		}
		if !c.MajorVersionBump {
			return fmt.Errorf("this is a major version update (v%d -> v%d), but neither --major nor --allow-major was passed",
				repo.currentUpstreamVersion.Major(), upgradeTarget.Version.Major())
		}
	}
	return nil
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

		if GetContext(ctx).UpgradeProviderVersion {
			err := applyMajorVersionPolicy(ctx, &repo, upgradeTarget)
			stepv2.HaltOnError(ctx, err)
		}

		if GetContext(ctx).MajorVersionBump {
			repo.currentVersion = findCurrentMajorVersion(ctx, repoOrg, repoName)
		}
	})
	if err != nil {
		return err
	}

	// Running the discover steps might have invalidated one or more actions. If there
	// are no actions remaining, we can exit early.
	if ctx := GetContext(ctx); !ctx.UpgradeBridgeVersion && !ctx.UpgradeProviderVersion &&
		ctx.TargetPulumiVersion == nil {
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
			// pulumi-java was renamed from pulumi-java/pkg to pulumi-java in
			// pulumi-java#2121. The legacy module's last release (v1.22.0) is
			// incompatible with pulumi/pulumi v3.232.0+. Go resolves the import
			// path github.com/pulumi/pulumi-java/pkg/codegen/java to whichever
			// module has the longest matching prefix, so as long as a stale
			// `pulumi-java/pkg` require remains, it shadows the new module.
			// No-op once the require is gone.
			step.Cmd("go", "mod", "edit",
				"-droprequire", "github.com/pulumi/pulumi-java/pkg"),
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
			upgrade("sdk").In(repo.sdkDir()),
		))
	}

	if GetContext(ctx).UpgradeBridgeVersion && GetContext(ctx).TargetPulumiVersion == nil {
		// Having changed the version of pulumi/{sdk,pkg} that we are using, we
		// need to propagate that change to the go.mod in {sdk,examples}/go.mod
		//
		// We make sure that TargetPulumiVersion == "", since we cannot discover
		// the version of a replace statement.
		steps = append(steps, applyPulumiVersion(ctx, repo))
	}

	// Patched-provider upgrades run scripts/upstream.sh, whose checkout command
	// creates commits inside the upstream submodule. Resolve identity immediately
	// before entering that flow so git am and the later Git commands can commit.
	if GetContext(ctx).UpgradeProviderVersion && goMod.Kind.IsPatched() {
		ctx, err = applyGitIdentityPreflight(ctx, repo.root)
		if err != nil {
			return err
		}
	}

	ok := step.Run(ctx, step.Combined("Update Repository", steps...))
	if !ok {
		return ErrHandled
	}

	var newPrURL string
	err = stepv2.PipelineCtx(ctx, "Tfgen & Build SDKs",
		tfgenAndBuildSDKs(repo, repoName, upgradeTarget, goMod,
			targetBridgeVersion, tfSDKUpgrade, &newPrURL))
	if err != nil {
		return err
	}

	if GetContext(ctx).NoSubmit {
		// Build the same plan used by InformGitHub, but render it only after the
		// pipeline and spinner have completed.
		plan := newGitHubSubmissionPlan(
			ctx, upgradeTarget, repo, goMod, targetBridgeVersion, tfSDKUpgrade, os.Args,
		)
		fmt.Print(noSubmitOutput(repo, plan, inspectLocalUpgrade(ctx, repo)))
	} else if newPrURL != "" {
		fmt.Printf("Link to PR created: %s\n", newPrURL)
	}

	return nil
}

// localUpgradeState contains measured Git state used in the no-submit report.
// String fields allow inspection failures to be represented as "unknown"
// without turning an otherwise successful local upgrade into a failure.
type localUpgradeState struct {
	// BaseRef is the remote-tracking ref used by the review commands.
	BaseRef string
	// WorkingTree is "clean", "dirty", or "unknown".
	WorkingTree string
	// CommitsAhead is the number of commits in HEAD but not BaseRef, or "unknown".
	CommitsAhead string
}

// inspectLocalUpgrade measures the final checkout without mutating it. Git
// inspection is best-effort because failure to produce advisory output should
// not fail a completed upgrade.
func inspectLocalUpgrade(ctx context.Context, repo ProviderRepo) localUpgradeState {
	state := localUpgradeState{
		BaseRef:      "origin/" + repo.defaultBranch,
		WorkingTree:  "unknown",
		CommitsAhead: "unknown",
	}

	// -C avoids changing the process working directory after the pipeline has
	// restored it.
	gitOutput := func(args ...string) (string, error) {
		cmdArgs := append([]string{"-C", repo.root}, args...)
		out, err := exec.CommandContext(ctx, "git", cmdArgs...).Output()
		return strings.TrimSpace(string(out)), err
	}

	if status, err := gitOutput("status", "--porcelain=1"); err == nil {
		if status == "" {
			state.WorkingTree = "clean"
		} else {
			state.WorkingTree = "dirty"
		}
	}
	if count, err := gitOutput("rev-list", "--count", state.BaseRef+"..HEAD"); err == nil {
		state.CommitsAhead = count
	}

	return state
}

// noSubmitOutput renders a complete, copyable review and submission checklist.
// The PR body is emitted verbatim so it matches normal GitHub submission.
func noSubmitOutput(repo ProviderRepo, plan githubSubmissionPlan, state localUpgradeState) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Upgrade completed locally; no branch was pushed and no PR was created or updated.")
	fmt.Fprintln(&b)

	// field keeps summary and PR metadata aligned while making absent optional
	// values explicit to an agent reading the report.
	field := func(name, value string) {
		if value == "" {
			value = "(none)"
		}
		fmt.Fprintf(&b, "  %-22s %s\n", name+":", value)
	}
	field("Repository", plan.Repository)
	field("Local path", repo.root)
	field("Base branch", plan.BaseBranch)
	field("Working branch", plan.WorkingBranch)
	field("Working tree", state.WorkingTree)
	field("Commits ahead", fmt.Sprintf("%s (%s)", state.CommitsAhead, state.BaseRef))
	// Combined upgrades can have more than one target, so render one transition
	// per dependency rather than a misleading singular target version.
	if len(plan.Targets) > 0 {
		fmt.Fprintln(&b, "  Upgrade targets:")
		for _, target := range plan.Targets {
			fmt.Fprintf(&b, "    - %s\n", target)
		}
	}

	// Everything in this section comes from the same plan consumed by gh.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Proposed PR")
	fmt.Fprintln(&b, "-----------")
	if plan.ExistingPR {
		field("Action", "Update existing PR")
	} else {
		field("Action", "Create PR")
	}
	field("Title", plan.Title)
	field("Labels", plan.Label)
	field("Reviewers", plan.Reviewers)
	field("Assignee", plan.Assignee)
	if len(plan.IssueAssignments) == 0 {
		field("Issue assignments", "(none)")
	} else {
		issues := make([]string, len(plan.IssueAssignments))
		for i, issue := range plan.IssueAssignments {
			issues[i] = fmt.Sprintf("#%d -> %s", issue, plan.Assignee)
		}
		field("Issue assignments", strings.Join(issues, ", "))
	}
	if plan.CloseSupersededBridgePRs {
		field("Superseded bridge PRs", "close open bridge upgrade PRs authored by @me, except this branch")
	} else {
		field("Superseded bridge PRs", "(none)")
	}
	// Do not indent or otherwise transform the body: users should be able to
	// compare or copy the exact text that gh would receive.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Body:")
	fmt.Fprint(&b, plan.Body)
	if !strings.HasSuffix(plan.Body, "\n") {
		fmt.Fprintln(&b)
	}

	// Review commands use the same remote base used for the measured commit count.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Review with:")
	fmt.Fprintf(&b, "  git log --oneline %s..HEAD\n", state.BaseRef)
	fmt.Fprintf(&b, "  git diff --stat %s...HEAD\n", state.BaseRef)
	fmt.Fprintf(&b, "  git diff %s...HEAD\n", state.BaseRef)

	// Spell out non-PR side effects as a checklist so an agent does not stop
	// after merely creating or updating the PR.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Default submission actions skipped:")
	fmt.Fprintf(&b, "  1. git push --set-upstream origin %s --force\n", plan.WorkingBranch)
	if plan.ExistingPR {
		fmt.Fprintln(&b, "  2. Update the existing PR with the title, body, labels, reviewers, and assignee above.")
	} else {
		fmt.Fprintln(&b, "  2. Create the PR with the base, head, title, body, labels, reviewers, and assignee above.")
	}
	nextAction := 3
	if len(plan.IssueAssignments) > 0 {
		fmt.Fprintf(&b, "  %d. Assign the listed issues to the PR assignee.\n", nextAction)
		nextAction++
	}
	if plan.CloseSupersededBridgePRs {
		fmt.Fprintf(&b,
			"  %d. Close open bridge upgrade PRs authored by @me, except this branch, and comment with the replacement PR URL.\n",
			nextAction,
		)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Submit manually with Git and GitHub CLI after review.")
	return b.String()
}

func tfgenAndBuildSDKs(
	repo ProviderRepo, repoName string, upgradeTarget *UpstreamUpgradeTarget, goMod *GoMod,
	targetBridgeVersion Ref, tfSDKUpgrade string, newPrURL *string,
) func(ctx context.Context) {
	return func(ctx context.Context) {
		env := []stepv2.Env{&stepv2.SetCwd{To: repo.root}}
		ctx = stepv2.WithEnv(ctx, env...)

		miseEnvVars := map[string]*stepv2.EnvVar{}

		miseAvailable := false
		ctx, miseAvailable = runMiseUpgrade(ctx, repo, &env, miseEnvVars)
		if miseAvailable {
			ctx = refreshMiseEnv(ctx, &env, miseEnvVars)
		}

		stepv2.WithCwd(ctx, *repo.providerDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		stepv2.WithCwd(ctx, *repo.examplesDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		stepv2.WithCwd(ctx, *repo.sdkDir(), func(ctx context.Context) {
			stepv2.Cmd(ctx, "go", "mod", "tidy")
		})

		if miseAvailable {
			if updatedCtx, ok := runMiseUpgrade(ctx, repo, &env, miseEnvVars); ok {
				ctx = updatedCtx
				ctx = refreshMiseEnv(ctx, &env, miseEnvVars)
			}
		}

		stepv2.Cmd(ctx, "pulumi", "plugin", "rm", "--all", "--yes")

		stepv2.Cmd(ctx, "make", "tfgen")

		stepv2.Cmd(ctx, "git", "add", "--all")
		gitCommit(ctx, "make tfgen")

		gen := "generate_sdks"

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

		*newPrURL = InformGitHub(ctx, upgradeTarget, repo, goMod, targetBridgeVersion, tfSDKUpgrade, os.Args)
	}
}

func runMiseUpgrade(
	ctx context.Context, repo ProviderRepo, env *[]stepv2.Env, current map[string]*stepv2.EnvVar,
) (context.Context, bool) {
	if _, err := exec.LookPath("mise"); err != nil {
		stepv2.SetLabel(ctx, "mise not found; skipping upgrade")
		return ctx, false
	}

	pulumiVersion, err := pulumiVersionFromProvider(repo)
	stepv2.HaltOnError(ctx, err)
	goVersion, err := goVersionFromProvider(repo)
	stepv2.HaltOnError(ctx, err)

	version := strings.TrimPrefix(pulumiVersion, "v")

	// Capture the previous (pre-bump) versions before setMiseEnv overrides
	// them. We uninstall them after the new versions are installed so that
	// PATH lookup doesn't shadow the new install dir with the stale one.
	oldPulumi := os.Getenv("PULUMI_VERSION_MISE")
	oldGo := os.Getenv("GO_VERSION_MISE")

	ctx = setMiseEnv(ctx, env, current, "PULUMI_VERSION_MISE", version)
	ctx = setMiseEnv(ctx, env, current, "GO_VERSION_MISE", goVersion)
	ctx = setMiseEnv(ctx, env, current, "MISE_TRUSTED_CONFIG_PATHS", repo.root)
	ctx = setMiseEnv(ctx, env, current, "MISE_YES", "1")

	stepv2.Cmd(ctx, "mise", "install")
	stepv2.Cmd(ctx, "mise", "upgrade", "--raw")

	// Remove the stale pre-bump install dirs from disk. `mise install` adds
	// the new install dirs to PATH but does not remove the old ones, and the
	// old entries appear ahead of the new ones, so without this PATH lookup
	// keeps finding the stale binary.
	if oldPulumi != "" && oldPulumi != version {
		stepv2.Cmd(ctx, "mise", "uninstall", "github:pulumi/pulumi@"+oldPulumi)
	}
	if oldGo != "" && oldGo != goVersion {
		stepv2.Cmd(ctx, "mise", "uninstall", "go@"+oldGo)
	}

	return ctx, true
}

func setMiseEnv(
	ctx context.Context, env *[]stepv2.Env, current map[string]*stepv2.EnvVar, key, value string,
) context.Context {
	if existing, ok := current[key]; ok {
		existing.Value = value
		return ctx
	}

	envVar := &stepv2.EnvVar{Key: key, Value: value}
	current[key] = envVar
	*env = append(*env, envVar)
	return stepv2.WithEnv(ctx, envVar)
}

func refreshMiseEnv(ctx context.Context, env *[]stepv2.Env, current map[string]*stepv2.EnvVar) context.Context {
	miseEnvRaw := stepv2.Cmd(ctx, "mise", "env", "--json")
	var miseEnv map[string]string
	err := json.Unmarshal([]byte(miseEnvRaw), &miseEnv)
	stepv2.HaltOnError(ctx, err)

	for key, value := range miseEnv {
		if existing, ok := current[key]; ok {
			existing.Value = value
			continue
		}

		envVar := &stepv2.EnvVar{Key: key, Value: value}
		current[key] = envVar
		*env = append(*env, envVar)
		ctx = stepv2.WithEnv(ctx, envVar)
	}

	return ctx
}
