package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestInformGithub(t *testing.T) {
	ctx := newReplay(t, "wavefront_inform_github")

	ctx = (&Context{
		UpgradeProviderVersion: true,
		PrAssign:               "@me",
		PrReviewers:            "pulumi/Providers,lukehoban",
		UpstreamProviderName:   "terraform-provider-wavefront",
		UpstreamProviderOrg:    "vmware",
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
				prTitle:                "Upgrade terraform-provider-wavefront to v5.0.5",
				defaultBranch:          "master",
				Name:                   "pulumi/pulumi-wavefront",
				currentUpstreamVersion: semver.MustParse("5.0.3"),
			}, &GoMod{
				Kind: "plain",
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

func TestInformGithubExistingPR(t *testing.T) {
	ctx := newReplay(t, "kong_existing_pr")

	ctx = (&Context{
		PrAssign:             "@me",
		PrReviewers:          "pulumi/Providers,lukehoban",
		UpstreamProviderName: "terraform-provider-kong",
		UpstreamProviderOrg:  "kevholditch",
		UpgradeBridgeVersion: true,
	}).Wrap(ctx)

	err := step.PipelineCtx(ctx, "Tfgen & Build SDKs", func(ctx context.Context) {
		InformGitHub(ctx,
			nil, ProviderRepo{
				workingBranch:   "upgrade-pulumi-terraform-bridge-to-v3.62.0",
				prTitle:         "Upgrade pulumi-terraform-bridge to v3.62.0",
				defaultBranch:   "master",
				Name:            "pulumi/pulumi-kong",
				prAlreadyExists: true,
			}, &GoMod{
				Kind: "plain",
				Upstream: module.Version{
					Path:    "github.com/kevholditch/terraform-provider-kong",
					Version: "v1.9.2-0.20220328204855-9e50bd93437f",
				},
				Bridge: module.Version{
					Path:    "github.com/pulumi/pulumi-terraform-bridge/v3",
					Version: "v3.60.0",
				},
			},
			&Version{SemVer: semver.MustParse("v3.62.0")},
			nil, "Up to date at 2.29.0",
			[]string{"upgrade-provider",
				"pulumi/pulumi-kong", "--kind=bridge"})
	})
	require.NoError(t, err)
}

func TestBridgeUpgradeNoop(t *testing.T) {
	ctx := newReplay(t, "gcp_noop_bridge_update")
	ctx = (&Context{
		GoPath:               "/goPath",
		UpgradeBridgeVersion: true,
		TargetBridgeRef:      &Latest{},
		UpstreamProviderOrg:  "hashicorp",
	}).Wrap(ctx)

	err := step.CallWithReplay(ctx, "Plan Upgrade",
		"Planning Bridge Upgrade", planBridgeUpgrade)
	require.NoError(t, err)

	assert.False(t, GetContext(ctx).UpgradeBridgeVersion)
}

func newReplay(t *testing.T, name string) context.Context {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join("testdata", "replay", name+".json")
	bytes := readFile(t, path)
	r := step.NewReplay(t, bytes)
	return step.WithEnv(ctx, r)
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	return bytes
}

func testReplay(ctx context.Context, t *testing.T, stepReplay []*step.Step, fName string, f any) {
	t.Helper()
	bytes, err := json.Marshal(step.ReplayV1{
		Pipelines: []step.RecordV1{{
			Name:  t.Name(),
			Steps: stepReplay,
		}},
	})
	require.NoError(t, err)

	r := step.NewReplay(t, bytes)
	ctx = step.WithEnv(ctx, r)

	err = step.CallWithReplay(ctx, t.Name(), fName, f)
	assert.NoError(t, err)
}

func jsonMarshal[T any](t *testing.T, content string) T {
	t.Helper()
	var dst T
	err := json.Unmarshal([]byte(content), &dst)
	require.NoError(t, err)
	return dst
}
