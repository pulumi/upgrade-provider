package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/hexops/autogold/v2"
	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/assert"
	"golang.org/x/mod/module"
)

func TestGetRepoExpectedLocation(t *testing.T) {
	ctx := &Context{
		GoPath: "/Users/myuser/go",
	}

	mockRepoPath := filepath.Join("github.com", "pulumi", "random-provider")
	defaultExpectedLocation := filepath.Join(ctx.GoPath, "src", mockRepoPath)

	baseProviderCwd := string(os.PathSeparator) + filepath.Join("Users", "home", mockRepoPath)
	subProviderCwd := filepath.Join(baseProviderCwd, "examples")
	randomCwd := string(os.PathSeparator) + filepath.Join("Users", "random", "dir")

	// test cwd == repo path
	tests := []struct{ cwd, repoPath, expected string }{
		{baseProviderCwd, mockRepoPath, baseProviderCwd},   // expected set to cwd
		{subProviderCwd, mockRepoPath, baseProviderCwd},    // expected set to top level of cwd repo path
		{randomCwd, mockRepoPath, defaultExpectedLocation}, // expected set to default on no match
	}

	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprintf("(%s,%s,%s)", tt.cwd, tt.repoPath, tt.expected), func(t *testing.T) {
			expected, err := getRepoExpectedLocation(ctx.Wrap(context.Background()), tt.cwd, tt.repoPath)
			expected = trimSeparators(expected)
			assert.Nil(t, err)
			assert.Equal(t, trimSeparators(tt.expected), expected)
		})
	}
}

func trimSeparators(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, string(os.PathSeparator)),
		string(os.PathSeparator))
}

func TestPullRequestBody(t *testing.T) {
	t.Run("description-space", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("description-equal", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description=" + uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("upgrades", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{
			UpgradePfVersion:     true,
			UpgradeBridgeVersion: true,
		}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, &GoMod{
			Bridge: module.Version{Version: "v1.2.2"},
			Pf:     module.Version{Version: "v4.5.5"},
		},
			&Version{SemVer: semver.MustParse("v1.2.3")},
			&Version{SemVer: semver.MustParse("v4.5.6")}, "", args)
		autogold.ExpectFile(t, got)
	})
}

func TestGetExpectedTargetFromUpstream(t *testing.T) {
	repo, name := "pulumi/pulumi-cloudflare", "cloudflare"

	simpleReplay(t, jsonMarshal[[]*step.Step](t, `[
  {
    "name": "Get Expected Target",
    "inputs": [
      "`+repo+`",
      "`+name+`"
    ],
    "outputs": [
      {
        "Version": "4.19.0",
        "GHIssues": null
      },
      null
    ]
  },
  {
    "name": "From Upstream Releases",
    "inputs": [
      "pulumi/pulumi-cloudflare",
      "cloudflare"
    ],
    "outputs": [
      {
        "Version": "4.19.0",
        "GHIssues": null
      },
      null
    ]
  },
  {
    "name": "gh",
    "inputs": [
      "gh",
      [
        "release",
        "list",
        "--repo=cloudflare/terraform-provider-cloudflare",
        "--limit=1",
        "--exclude-drafts",
        "--exclude-pre-releases"
      ]
    ],
    "outputs": [
      "v4.19.0\tLatest\tv4.19.0\t2023-11-14T23:37:22Z\n",
      null
    ],
    "impure": true
  }
]`), func(ctx context.Context) {
		context := &Context{
			GoPath:               "/Users/myuser/go",
			UpstreamProviderName: "terraform-provider-cloudflare",
		}
		result := getExpectedTarget(context.Wrap(ctx),
			repo, name)
		assert.NotNil(t, result)
	})
}

func TestGetExpectedTargetFromTarget(t *testing.T) {
	repo, name := "pulumi/pulumi-cloudflare", "cloudflare"
	test := func(testName string, inferVersion bool, targetVersion, expected string) {
		t.Run(testName, func(t *testing.T) {
			expectedSteps := jsonMarshal[[]*step.Step](t, expected)
			simpleReplay(t, expectedSteps, func(ctx context.Context) {
				context := &Context{
					GoPath:               "/Users/myuser/go",
					UpstreamProviderName: "terraform-provider-cloudflare",
					InferVersion:         inferVersion,
					TargetVersion:        semver.MustParse(targetVersion),
				}
				result := getExpectedTarget(context.Wrap(ctx),
					repo, name)
				assert.NotNil(t, result)
			})
		})
	}

	test("expected-target", false, "4.19.0", `[
  {
    "name": "Get Expected Target",
    "inputs": [
      "`+repo+`",
      "`+name+`"
    ],
    "outputs": [
      {
        "Version": "4.19.0",
        "GHIssues": null
      },
      null
    ]
  }
]`)

	// The replay for when "Get Expected Target" is called with `Context.InferVersion
	// = true`.
	expectedTargetWithIssues := func(output string) string {
		return `[
  {
    "name": "Get Expected Target",
    "inputs": [
      "` + repo + `",
      "` + name + `"
    ],
    "outputs": [
      ` + output + `,
      null
    ]
  },
  {
    "name": "From Issues",
    "inputs": [
      "` + repo + `"
    ],
    "outputs": [
      {
        "Version": "2.32.0",
        "GHIssues": [
          {
            "number": 540
          },
          {
            "number": 538
          }
        ]
      },
      null
    ]
  },
  {
    "name": "gh",
    "inputs": [
      "gh",
      [
        "issue",
        "list",
        "--state=open",
        "--author=pulumi-bot",
        "--repo=pulumi/pulumi-cloudflare",
        "--limit=100",
        "--json=title,number"
      ]
    ],
    "outputs": [
      "[{\"number\":540,\"title\":\"Upgrade terraform-provider-cloudflare to v2.32.0\"},{\"number\":538,\"title\":\"Upgrade terraform-provider-cloudflare to v2.31.0\"}]\n",
      null
    ],
    "impure": true
  }
]`
	}

	test("expected-target-all-issues", true, "v2.32.0",
		expectedTargetWithIssues(`{
  "Version": "2.32.0",
  "GHIssues": [
    { "number": 540 },
    { "number": 538 }
  ]
}`))

	test("expected-target-some-issues", true, "v2.31.0",
		expectedTargetWithIssues(`{
  "Version": "2.31.0",
  "GHIssues": [{ "number": 538 }]
}`))

	test("expected-target-no-issues", true, "v2.30.0",
		expectedTargetWithIssues(`{
  "Version": "2.30.0",
  "GHIssues": null
}`))
}
