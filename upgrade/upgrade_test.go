package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"
)

func TestInformGithub(t *testing.T) {
	ctx := newReplay(t, "wavefront-inform-github")

	ctx = (&Context{
		UpgradeProviderVersion: true,
		PrAssign:               "@me",
		PrReviewers:            "pulumi/Providers,lukehoban",
		UpstreamProviderName:   "terraform-provider-wavefront",
	}).Wrap(ctx)

	err := step.PipelineCtx(ctx, "Tfgen & Build SDKs", func(ctx context.Context) {
		InformGitHub(ctx,
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

func newReplay(t *testing.T, name string) context.Context {
	ctx := context.Background()
	path := filepath.Join("testdata", "replay", name+".json")
	bytes := readFile(t, path)
	r := step.NewReplay(t, bytes)
	return step.WithEnv(ctx, r)
}

func readFile(t *testing.T, path string) []byte {
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	return bytes
}
