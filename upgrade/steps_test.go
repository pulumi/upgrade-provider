package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"

	"github.com/pulumi/upgrade-provider/step/v2"
)

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

			encode := func(elem any) json.RawMessage {
				b, err := json.Marshal(elem)
				require.NoError(t, err)
				return json.RawMessage(b)
			}

			testReplay(context.Background(), t, []*step.Step{
				{
					Name:    "Has Existing PR",
					Inputs:  encode([]string{tt.branchName, "pulumi/pulumi-xyz"}),
					Outputs: encode([]any{tt.expect, nil}),
				},
				{
					Name: "gh",
					Inputs: encode([]any{
						"gh", []string{"pr", "list", "--json=title,headRefName", "--repo=pulumi/pulumi-xyz"},
					}),
					Outputs: encode([]any{tt.response, nil}),
					Impure:  true,
				},
			}, "Has Existing PR", hasExistingPr)
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

			testReplay(context.Background(), t, replay,
				"Ensure Branch", ensureBranchCheckedOut)
		})
	}
}

func TestEnsureUpstreamRepo(t *testing.T) {
	ctx := newReplay(t, "download_aiven")
	err := step.CallWithReplay((&Context{GoPath: "/goPath"}).Wrap(ctx), "Discover Provider",
		"Ensure Upstream Repo", ensureUpstreamRepo)
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

func TestUpstreamGoModRegexp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    module.Version
		expected string
	}{
		{
			name: "Gets plain upstream org name with v1 provider format",
			input: module.Version{
				Path: "github.com/unicornio/terraform-provider-foo",
			},
			expected: "unicornio",
		},
		{
			name: "Gets allowed chars in upstream org name with v1 provider format",
			input: module.Version{
				Path: "github.com/uniCorn_12-io/terraform-provider-foo",
			},
			expected: "uniCorn_12-io",
		},
		{
			name: "Gets plain upstream org name with v2+ provider format",
			input: module.Version{
				Path: "github.com/unicornio/terraform-provider-foo/v2",
			},
			expected: "unicornio",
		},
		{
			name: "Gets allowed chars in upstream org name with v2+ provider format",
			input: module.Version{
				Path: "github.com/uniCorn_12-io/terraform-provider-foo/v45",
			},
			expected: "uniCorn_12-io",
		},
		{
			name: "Gets non-GitHub hosted module name",
			input: module.Version{
				Path: "gitlab.com/unicornio/terraform-provider-foo/v2",
			},
			expected: "unicornio",
		},
		{
			name: "Errors on invalid path",
			input: module.Version{
				Path: "terraform-provider-foo",
			},
			expected: "",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := step.Pipeline("test", func(ctx context.Context) {
				actual := parseUpstreamProviderOrg(ctx, tt.input)
				assert.Equal(t, tt.expected, actual)
			})
			if tt.name == "Errors on invalid path" {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseUpstreamProviderOrgFromModVersion(t *testing.T) {
	testReplay((&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-datadog",
		UpstreamProviderOrg:  "",
	}).Wrap(context.Background()), t, jsonMarshal[[]*step.Step](t, `[
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
]`), "Get UpstreamOrg from module version", parseUpstreamProviderOrg)
}

func TestCheckMaintenancePatchWithinCadence(t *testing.T) {
	fourWeeksAgo := time.Now().Add(-time.Hour * 24 * 7 * 4).Format(time.RFC3339)
	testReplay((&Context{
		GoPath: "/Users/myuser/go",
	}).Wrap(context.Background()),
		t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Check if we should release a maintenance patch",
          "inputs": [
            {
				"Org":  "pulumi",
				"Name": "pulumi-cloudinit"
			}
          ],
          "outputs": [
            false,
	    null
          ]
        },
        {
          "name": "gh",
          "inputs": [
            "gh",
            [
              "repo",
              "view",
              "pulumi/pulumi-cloudinit",
              "--json=latestRelease"
            ]
          ],
          "outputs": [
            "{\"latestRelease\":{\"name\":\"v1.4.0\",\"tagName\":\"v1.4.0\",\"url\":\"https://github.com/pulumi/pulumi-cloudinit/releases/tag/v1.4.0\",\"publishedAt\":\"`+fourWeeksAgo+`\"}}\n",
            null
          ],
          "impure": true
        }
]`), "Check if we should release a maintenance patch", maintenanceRelease)
}

func TestCheckMaintenancePatchExpiredCadence(t *testing.T) {
	testReplay((&Context{
		GoPath: "/Users/myuser/go",
	}).Wrap(context.Background()),
		t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Check if we should release a maintenance patch",
          "inputs": [
            {
				"Org":  "pulumi",
				"Name": "pulumi-cloudinit"
			}
          ],
          "outputs": [
            true,
			null
          ]
        },
        {
          "name": "gh",
          "inputs": [
            "gh",
            [
              "repo",
              "view",
              "pulumi/pulumi-cloudinit",
              "--json=latestRelease"
            ]
          ],
          "outputs": [
			"{\"latestRelease\":{\"name\":\"v1.4.0\",\"tagName\":\"v1.4.0\",\"url\":\"https://github.com/pulumi/pulumi-cloudinit/releases/tag/v1.4.0\",\"publishedAt\":\"2023-01-04T21:03:48Z\"}}\n",            null
          ],
          "impure": true
        }
]`), "Check if we should release a maintenance patch", maintenanceRelease)
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
	testReplay((&Context{GoPath: "/Users/myuser/go"}).Wrap(ctx), t, jsonMarshal[[]*step.Step](t, `
	[
	  {
	    "name": "Planning Plugin SDK Upgrade",
	    "inputs": [
	      "3.73.0"
	    ],
	    "outputs": [
	      "v2.0.0-20240129205329-74776a5cd5f9",
	      "bridge 3.73.0 needs terraform-plugin-sdk v2.0.0-20240129205329-74776a5cd5f9",
	      null
	    ]
	  }
	]`), "Planning Plugin SDK Upgrade", planPluginSDKUpgrade)
}

type simpleHttpHandler func(string) ([]byte, error)

var _ httpHandler = (*simpleHttpHandler)(nil)

func (sh simpleHttpHandler) getHTTP(url string) ([]byte, error) {
	return sh(url)
}
