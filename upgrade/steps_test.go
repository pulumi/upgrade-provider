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

	"github.com/pulumi/upgrade-provider/step/v2"
)

func jsonMarshal[T any](t *testing.T, content string) T {
	t.Helper()
	var dst T
	err := json.Unmarshal([]byte(content), &dst)
	require.NoError(t, err)
	return dst
}

func TestGetWorkingBranch(t *testing.T) {
	type test struct {
		c                   Context
		targetBridgeVersion Ref
		upgradeTarget       UpstreamUpgradeTarget
		branchSuffix        string

		expected    string
		expectedErr string
	}
	tests := []test{
		{
			c: Context{
				UpgradeProviderVersion: true,
				UpstreamProviderName:   "foo",
			},
			branchSuffix:  "",
			upgradeTarget: UpstreamUpgradeTarget{Version: semver.MustParse("1.2.3")},
			expected:      "upgrade-foo-to-v1.2.3",
		},
		{
			c: Context{
				UpgradeBridgeVersion: true,
				TargetBridgeRef:      &Version{SemVer: semver.MustParse("v1.2.3")},
			},
			branchSuffix:        "",
			targetBridgeVersion: &Version{SemVer: semver.MustParse("1.2.3")},
			expected:            "upgrade-pulumi-terraform-bridge-to-1.2.3",
		},
		{
			c: Context{
				UpgradeBridgeVersion: true,
				TargetBridgeRef:      &Version{SemVer: semver.MustParse("v1.2.3")},
				PRTitlePrefix:        "foo",
			},
			targetBridgeVersion: &Version{SemVer: semver.MustParse("1.2.3")},
			branchSuffix:        "foo",
			expected:            "upgrade-pulumi-terraform-bridge-to-1.2.3-foo",
		},
		{
			c: Context{
				UpgradeBridgeVersion: true,
				TargetBridgeRef:      &Version{SemVer: semver.MustParse("v1.2.3")},
				PRTitlePrefix:        "[DOWNSTREAM TEST] [PLATFORM]",
			},
			targetBridgeVersion: &Version{SemVer: semver.MustParse("1.2.3")},
			branchSuffix:        "[DOWNSTREAM TEST] [PLATFORM]",
			expected:            "upgrade-pulumi-terraform-bridge-to-1.2.3-downstreamtestplatform",
		},
		{expectedErr: "unknown action"}, // If no action can be produced, we should error.
	}

	testF := func(tt test) func(t *testing.T) {
		return func(t *testing.T) {
			t.Parallel()

			err := step.Pipeline(t.Name(), func(ctx context.Context) {
				actual := getWorkingBranch(ctx, tt.c, tt.targetBridgeVersion, &tt.upgradeTarget, tt.branchSuffix)
				if os.Getenv("CI") == "true" {
					assert.Regexp(t, "^"+tt.expected+"-ci$", actual)
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

			testRunner := &TestRunner{}
			ctx := (&Context{
				r: testRunner,
			}).Wrap(context.Background())
			testRunner.mockMap = map[string]RunResult{
				"gh pr list --json=title,headRefName --repo=pulumi/pulumi-xyz": {
					Output: tt.response,
				},
			}

			res := hasExistingPr(ctx, tt.branchName, "pulumi/pulumi-xyz")
			require.Equal(t, tt.expect, res)
		})
	}
}

func TestEnsureBranchCheckedOut(t *testing.T) {
	t.Parallel()

	t.Run("does not exist", func(t *testing.T) {
		t.Parallel()

		testRunner := &TestRunner{}
		ctx := (&Context{
			r: testRunner,
		}).Wrap(context.Background())
		testRunner.mockMap = map[string]RunResult{
			"git branch": {Output: "* master\n"},
			"git checkout -b upgrade-pulumi-terraform-bridge-to-v3.62.0": {Output: ""},
		}

		res := ensureBranchCheckedOut(ctx, "upgrade-pulumi-terraform-bridge-to-v3.62.0")
		require.NoError(t, res)
	})

	t.Run("already exists", func(t *testing.T) {
		t.Parallel()

		testRunner := &TestRunner{}
		ctx := (&Context{
			r: testRunner,
		}).Wrap(context.Background())
		testRunner.mockMap = map[string]RunResult{
			"git branch": {Output: "* master\n  upgrade-pulumi-terraform-bridge-to-v3.62.0\n"},
			"git checkout upgrade-pulumi-terraform-bridge-to-v3.62.0": {Output: ""},
		}

		res := ensureBranchCheckedOut(ctx, "upgrade-pulumi-terraform-bridge-to-v3.62.0")
		require.NoError(t, res)
	})

	t.Run("already current", func(t *testing.T) {
		t.Parallel()

		testRunner := &TestRunner{}
		ctx := (&Context{
			r: testRunner,
		}).Wrap(context.Background())
		testRunner.mockMap = map[string]RunResult{
			"git branch": {Output: "* upgrade-pulumi-terraform-bridge-to-v3.62.0\n"},
			"git checkout upgrade-pulumi-terraform-bridge-to-v3.62.0": {Output: ""},
		}

		res := ensureBranchCheckedOut(ctx, "upgrade-pulumi-terraform-bridge-to-v3.62.0")
		require.NoError(t, res)
	})
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

func TestPluginSDKUpgrade(t *testing.T) {
	ctx := context.WithValue(context.Background(), httpHandlerKey, simpleHttpHandler(func(url string) ([]byte, error) {
		if url == "https://raw.githubusercontent.com/pulumi/pulumi-terraform-bridge/v3.73.0/go.mod" {
			return []byte(`
module github.com/pulumi/pulumi-terraform-bridge/v3

go 1.20

replace github.com/pulumi/pulumi-terraform-bridge/x/muxer => ./x/muxer

replace github.com/hashicorp/terraform-plugin-sdk/v2 => github.com/pulumi/terraform-plugin-sdk/v2 v2.0.0-20240129205329-74776a5cd5f9
`), nil
		}
		return nil, fmt.Errorf("not found")
	}))

	res, display, err := planPluginSDKUpgrade(ctx, "3.73.0")
	require.NoError(t, err)
	require.Equal(t, "v2.0.0-20240129205329-74776a5cd5f9", res)
	require.Equal(t, "bridge 3.73.0 needs terraform-plugin-sdk v2.0.0-20240129205329-74776a5cd5f9", display)
}

type simpleHttpHandler func(string) ([]byte, error)

var _ httpHandler = (*simpleHttpHandler)(nil)

func (sh simpleHttpHandler) getHTTP(url string) ([]byte, error) {
	return sh(url)
}
