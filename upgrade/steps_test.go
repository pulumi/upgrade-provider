package upgrade

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWorkingBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		c                                    Context
		targetBridgeVersion, targetPfVersion Ref
		upgradeTarget                        UpstreamUpgradeTarget

		expected    string
		expectedErr string
	}{
		{
			c: Context{
				UpgradeProviderVersion: true,
				UpstreamProviderName:   "foo",
			},
			upgradeTarget: UpstreamUpgradeTarget{Version: semver.MustParse("1.2.3")},
			expected:      "upgrade-foo-to-v1.2.3",
		},
		{
			c: Context{
				UpgradeBridgeVersion: true,
				TargetBridgeRef:      &Version{SemVer: semver.MustParse("v1.2.3")},
			},
			targetBridgeVersion: &Version{SemVer: semver.MustParse("1.2.3")},
			expected:            "upgrade-pulumi-terraform-bridge-to-1.2.3",
		},
		{expectedErr: "unknown action"}, // If no action can be produced, we should error.
	}

	for _, tt := range tests {
		tt := tt
		t.Run("", func(t *testing.T) {
			t.Parallel()

			err := step.Pipeline(t.Name(), func(ctx context.Context) {
				actual := getWorkingBranch(ctx, tt.c, tt.targetBridgeVersion, tt.targetPfVersion, &tt.upgradeTarget)
				assert.Equal(t, tt.expected, actual)
			})

			if tt.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.expectedErr)
			}
		})
	}
}

func TestHasRemoteBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		responce   string
		branchName string
		expect     bool
	}{
		{
			responce:   `[{"headRefName":"dependabot/go_modules/provider/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.13.0 to 0.17.0 in /provider"},{"headRefName":"dependabot/go_modules/sdk/golang.org/x/net-0.17.0","title":"bump golang.org/x/net from 0.10.0 to 0.17.0 in /sdk"},{"headRefName":"dependabot/go_modules/examples/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.8.0 to 0.17.0 in /examples"}]`,
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			expect:     false,
		},
		{
			responce:   `[{"headRefName":"upgrade-pulumi-terraform-bridge-to-v3.62.0","title":"Upgrade pulumi-terraform-bridge to v3.62.0"},{"headRefName":"dependabot/go_modules/provider/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.13.0 to 0.17.0 in /provider"},{"headRefName":"dependabot/go_modules/sdk/golang.org/x/net-0.17.0","title":"bump golang.org/x/net from 0.10.0 to 0.17.0 in /sdk"},{"headRefName":"dependabot/go_modules/examples/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.8.0 to 0.17.0 in /examples"}]`,
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			expect:     true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("", func(t *testing.T) {
			t.Parallel()

			encode := func(elem any) json.RawMessage {
				b, err := json.Marshal(elem)
				require.NoError(t, err)
				return json.RawMessage(b)
			}

			simpleReplay(t, step.RecordV1{
				Name: t.Name(),
				Steps: []*step.Step{
					{
						Name:    "Has Remote Branch",
						Inputs:  encode([]string{tt.branchName}),
						Outputs: encode([]any{tt.expect, nil}),
					},
					{
						Name: "gh",
						Inputs: encode([]any{
							"gh", []string{"pr", "list", "--json=title,headRefName"},
						}),
						Outputs: encode([]any{tt.responce, nil}),
						Impure:  true,
					},
				},
			}, func(ctx context.Context) { hasRemoteBranch(ctx, tt.branchName) })
		})
	}
}

func TestEnsureBranchCheckedOut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		responce   string
		branchName string
		call       []string
		namedValue string
	}{
		{
			responce:   "* master\n",
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			namedValue: "",
			call:       []string{"checkout", "-b", "upgrade-pulumi-terraform-bridge-to-v3.62.0"},
		},
		{
			responce:   "* master\n  upgrade-pulumi-terraform-bridge-to-v3.62.0\n",
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			namedValue: "already exists",
			call:       []string{"checkout", "upgrade-pulumi-terraform-bridge-to-v3.62.0"},
		},
		{
			responce:   "  master\n* upgrade-pulumi-terraform-bridge-to-v3.62.0\n",
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			namedValue: "already current",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("", func(t *testing.T) {
			t.Parallel()

			encode := func(elem any) json.RawMessage {
				b, err := json.Marshal(elem)
				require.NoError(t, err)
				return json.RawMessage(b)
			}

			replay := step.RecordV1{
				Name: t.Name(),
				Steps: []*step.Step{
					{
						Name:    "Ensure Branch",
						Inputs:  encode([]string{tt.branchName}),
						Outputs: encode([]any{nil}),
					},
					{
						Name: "git",
						Inputs: encode([]any{
							"git", []string{"branch"},
						}),
						Outputs: encode([]any{tt.responce, nil}),
						Impure:  true,
					},
					{
						Name:    tt.namedValue,
						Inputs:  encode([]any{}),
						Outputs: encode([]any{true, nil}),
					},
					{
						Name:    "git",
						Inputs:  encode([]any{"git", tt.call}),
						Outputs: encode([]any{"", nil}),
						Impure:  true,
					},
				},
			}

			if tt.namedValue == "" {
				replay.Steps = append(replay.Steps[:2], replay.Steps[3:]...)
			}
			if len(tt.call) == 0 {
				replay.Steps = replay.Steps[:len(replay.Steps)-1]
			}

			simpleReplay(t, replay, func(ctx context.Context) {
				ensureBranchCheckedOut(ctx, tt.branchName)
			})
		})
	}
}
