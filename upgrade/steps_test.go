package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestGetWorkingBranch(t *testing.T) {
	type test struct {
		c                                    Context
		targetBridgeVersion, targetPfVersion Ref
		upgradeTarget                        UpstreamUpgradeTarget

		expected    string
		expectedErr string
	}
	tests := []test{
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

	testF := func(tt test) func(t *testing.T) {
		return func(t *testing.T) {
			t.Parallel()

			err := step.Pipeline(t.Name(), func(ctx context.Context) {
				actual := getWorkingBranch(ctx, tt.c, tt.targetBridgeVersion, tt.targetPfVersion, &tt.upgradeTarget)
				if os.Getenv("CI") == "true" {
					assert.Regexp(t, "^"+tt.expected+"-[0-9]{8}$", actual)
				} else {
					assert.Equal(t, tt.expected, actual)
				}
			})

			if tt.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.expectedErr)
			}
		}
	}

	t.Run("CI=false", func(t *testing.T) {
		t.Setenv("CI", "false")
		for _, tt := range tests {
			tt := tt
			t.Run("", testF(tt))
		}
	})

	t.Run("CI=true", func(t *testing.T) {
		t.Setenv("CI", "true")
		for _, tt := range tests {
			tt := tt
			t.Run("", testF(tt))
		}
	})

}

func TestHasRemoteBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		response   string
		branchName string
		expect     bool
	}{
		{
			response:   `[{"headRefName":"dependabot/go_modules/provider/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.13.0 to 0.17.0 in /provider"},{"headRefName":"dependabot/go_modules/sdk/golang.org/x/net-0.17.0","title":"bump golang.org/x/net from 0.10.0 to 0.17.0 in /sdk"},{"headRefName":"dependabot/go_modules/examples/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.8.0 to 0.17.0 in /examples"}]`,
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			expect:     false,
		},
		{
			response:   `[{"headRefName":"upgrade-pulumi-terraform-bridge-to-v3.62.0","title":"Upgrade pulumi-terraform-bridge to v3.62.0"},{"headRefName":"dependabot/go_modules/provider/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.13.0 to 0.17.0 in /provider"},{"headRefName":"dependabot/go_modules/sdk/golang.org/x/net-0.17.0","title":"bump golang.org/x/net from 0.10.0 to 0.17.0 in /sdk"},{"headRefName":"dependabot/go_modules/examples/golang.org/x/net-0.17.0","title":"Bump golang.org/x/net from 0.8.0 to 0.17.0 in /examples"}]`,
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

			simpleReplay(t, []*step.Step{
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
					Outputs: encode([]any{tt.response, nil}),
					Impure:  true,
				},
			}, func(ctx context.Context) { hasRemoteBranch(ctx, tt.branchName) })
		})
	}
}

func TestEnsureBranchCheckedOut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		response   string
		branchName string
		call       []string
		namedValue string
	}{
		{
			response:   "* master\n",
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			namedValue: "",
			call:       []string{"checkout", "-b", "upgrade-pulumi-terraform-bridge-to-v3.62.0"},
		},
		{
			response:   "* master\n  upgrade-pulumi-terraform-bridge-to-v3.62.0\n",
			branchName: "upgrade-pulumi-terraform-bridge-to-v3.62.0",
			namedValue: "already exists",
			call:       []string{"checkout", "upgrade-pulumi-terraform-bridge-to-v3.62.0"},
		},
		{
			response:   "  master\n* upgrade-pulumi-terraform-bridge-to-v3.62.0\n",
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

			replay := []*step.Step{
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
					Outputs: encode([]any{tt.response, nil}),
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
			}

			if tt.namedValue == "" {
				replay = append(replay[:2], replay[3:]...)
			}
			if len(tt.call) == 0 {
				replay = replay[:len(replay)-1]
			}

			simpleReplay(t, replay, func(ctx context.Context) {
				ensureBranchCheckedOut(ctx, tt.branchName)
			})
		})
	}
}

func TestEnsureUpstreamRepo(t *testing.T) {
	ctx := newReplay(t, "download_aiven")
	err := step.PipelineCtx(ctx, "Discover Provider", func(ctx context.Context) {
		ctx = (&Context{
			GoPath: "/goPath",
		}).Wrap(ctx)
		ensureUpstreamRepo(ctx, "github.com/pulumi/pulumi-aiven")
	})
	require.NoError(t, err)
}

func TestReleaseLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to string
		expect   string
	}{
		// Same version
		{"v1.2.3", "v1.2.3", ""},
		{"v1.2.3", "v1.2.3+alpha", ""},

		// Upgrade
		{"v1.2.3", "v1.2.4", "needs-release/patch"}, // Patch
		{"v1.1.3", "v1.2.0", "needs-release/minor"}, // Minor+ Patch-
		{"v1.1.3", "v1.2.4", "needs-release/minor"}, // Minor+ Patch+
		{"1.2.4", "v2.0.0", "needs-release/major"},  // Major+ Minor- Patch-

		// Downgrades
		{"v2.1.3", "v1.2.4", ""}, // Major
		{"v1.1.3", "v1.0.4", ""}, // Minor
		{"v1.0.3", "v1.0.2", ""}, // Patch

		// Missing versions
		{"", "v1.2.4", ""},
		{"v2.2.3", "", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprintf("%s->%s", tt.from, tt.to), func(t *testing.T) {
			parse := func(s string) *semver.Version {
				if s == "" {
					return nil
				}
				return semver.MustParse(s)
			}

			from, to := parse(tt.from), parse(tt.to)

			err := step.Pipeline("test", func(ctx context.Context) {
				actual := upgradeLabel(ctx, from, to)
				assert.Equal(t, tt.expect, actual)
			})
			assert.NoError(t, err)
		})
	}
}

func TestParseUpstreamProviderOrgFromModVersion(t *testing.T) {

	upstreamVersion := module.Version{Path: "github.com/testing-org/terraform-provider-datadog", Version: "v0.0.0"}

	simpleReplay(t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Get UpstreamOrg from module version",
          "inputs": [
            {
              "Path": "github.com/testing-org/terraform-provider-datadog",
              "Version": "v0.0.0"
            }
          ],
          "outputs": [
            "testing-org",
            null
          ]
        }
]`), func(ctx context.Context) {
		context := &Context{
			GoPath:               "/Users/myuser/go",
			UpstreamProviderName: "terraform-provider-datadog",
			UpstreamProviderOrg:  "",
		}
		parseUpstreamProviderOrg(context.Wrap(ctx), upstreamVersion)
	})
}
