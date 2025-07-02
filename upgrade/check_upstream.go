package upgrade

import (
	"context"
	"fmt"

	semver "github.com/Masterminds/semver/v3"

	"github.com/pulumi/upgrade-provider/colorize"
	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

func CheckUpstream(ctx context.Context, repoOrg, repoName string, currentUpstreamVersion *semver.Version) (err error) {
	c := GetContext(ctx)
	if c.r == nil {
		c.r = &DefaultRunner{}
	}

	repo := ProviderRepo{
		Name:                   repoName,
		Org:                    repoOrg,
		currentUpstreamVersion: currentUpstreamVersion,
	}

	var goMod *GoMod
	var upgradeTarget *UpstreamUpgradeTarget

	err = stepv2.PipelineCtx(ctx, "Set Up Environment", func(ctx context.Context) {
		env := func(k, v string) { setEnv(ctx, k, v) }
		env("GOWORK", "off")
	})

	err = stepv2.PipelineCtx(ctx, "Discover Provider", func(ctx context.Context) {
		repo.root = ensureRepoInCWD(ctx, repoName)
		repo.defaultBranch = findDefaultBranch(ctx, "origin")
		goMod = getRepoKind(ctx, repo)

		if GetContext(ctx).UpgradeProviderVersion {
			upgradeTarget = planProviderUpgrade(ctx, repoOrg, repoName, goMod, &repo, nil)
		}
	})
	if err != nil {
		return err
	}

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
