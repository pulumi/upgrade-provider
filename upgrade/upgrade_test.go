package upgrade

import (
	"context"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"
)

func TestInformGithub(t *testing.T) {
	replay := step.NewReplay(t, []byte(`{
        "pipelines": [
            {
                "name": "Tfgen & Build SDKs",
                "steps": [
                    {
                        "name": "Inform Github",
                        "inputs": [
                            {
                                "Version": "5.0.5",
                                "GHIssues": [
                                    {
                                        "number": 232
                                    }
                                ]
                            },
                            {},
                            {
                                "Kind": "plain",
                                "Upstream": {
                                    "Path": "github.com/vmware/terraform-provider-wavefront",
                                    "Version": "v0.0.0-20231006183745-aa9a262f8bb0"
                                },
                                "Fork": null,
                                "Bridge": {
                                    "Path": "github.com/pulumi/pulumi-terraform-bridge/v3",
                                    "Version": "v3.61.0"
                                },
                                "Pf": {
                                    "Path": ""
                                },
                                "UpstreamProviderOrg": "vmware"
                            },
                            null,
                            null,
                            "Up to date at 2.29.0",
                            ["upgrade-provider", "pulumi/pulumi-wavefront"]
                        ],
                        "outputs": [
                            null
                        ]
                    },
                    {
                        "name": "git",
                        "inputs": [
                            "/opt/homebrew/bin/git push --set-upstream origin upgrade-terraform-provider-wavefront-to-v5.0.5"
                        ],
                        "outputs": [
                            "branch 'upgrade-terraform-provider-wavefront-to-v5.0.5' set up to track 'origin/upgrade-terraform-provider-wavefront-to-v5.0.5'.\n",
                            null
                        ],
                        "impure": true
                    },
                    {
                        "name": "gh",
    "inputs": [
      "/opt/homebrew/bin/gh pr create --assignee @me --base master --head upgrade-terraform-provider-wavefront-to-v5.0.5 --reviewer pulumi/Providers,lukehoban --title Upgrade terraform-provider-wavefront to v5.0.5 --body This PR was generated via `+"`$ upgrade-provider pulumi/pulumi-wavefront`"+`.\n\n---\n\n- Upgrading terraform-provider-wavefront from 5.0.3  to 5.0.5.\n\tFixes #232\n"
    ],
    "outputs": [
      "https://github.com/pulumi/pulumi-wavefront/pull/239\n",
      null
    ],
    "impure": true
  },
  {
    "name": "Assign Issues",
    "inputs": [],
    "outputs": [
      null
    ]
  },
  {
    "name": "gh",
    "inputs": [
      "/opt/homebrew/bin/gh issue edit 232 --add-assignee @me"
    ],
    "outputs": [
      "https://github.com/pulumi/pulumi-wavefront/issues/232\n",
      null
    ],
    "impure": true
  }
]
}
]
}`))

	ctx := (&Context{
		UpgradeProviderVersion: true,
		PrAssign:               "@me",
		PrReviewers:            "pulumi/Providers,lukehoban",
		UpstreamProviderName:   "terraform-provider-wavefront",
	}).Wrap(context.Background())

	err := step.PipelineCtx(step.WithEnv(ctx, replay), "Tfgen & Build SDKs", func(ctx context.Context) {
		step.Call70E(ctx, "Inform Github", InformGitHub,
			&UpstreamUpgradeTarget{
				GHIssues: []UpgradeTargetIssue{
					{Number: 232},
				},
				Version: semver.MustParse("5.0.5"),
			}, ProviderRepo{
				workingBranch:          "upgrade-terraform-provider-wavefront-to-v5.0.5",
				defaultBranch:          "master",
				name:                   "pulumi/pulumi-wavefront",
				currentUpstreamVersion: semver.MustParse("5.0.3"),
			}, &GoMod{
				UpstreamProviderOrg: "vmware",
				Kind:                "plain",
				Upstream: module.Version{
					Path:    "github.com/vmware/terraform-provider-wavefront",
					Version: "v0.0.0-20231006183745-aa9a262f8bb0",
				},
				Bridge: module.Version{
					Path:    "github.com/pulumi/pulumi-terraform-bridge/v3",
					Version: "v3.61.0",
				},
			}, nil, nil, "Up to date at 2.29.0", []string{"upgrade-provider", "pulumi/pulumi-wavefront"})
	})
	require.NoError(t, err)
}
