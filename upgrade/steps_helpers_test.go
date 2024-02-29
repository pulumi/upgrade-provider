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
	"github.com/stretchr/testify/assert"
	"golang.org/x/mod/module"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestRemoveVersionPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct{ input, expected string }{
		{ // No mod path
			"github.com/jfrog/terraform-provider-artifactory",
			"github.com/jfrog/terraform-provider-artifactory",
		},
		{ // Single digit mod path
			"github.com/jfrog/terraform-provider-artifactory/v4",
			"github.com/jfrog/terraform-provider-artifactory",
		},
		{ // Multi-digit mod path (10)
			"github.com/jfrog/terraform-provider-artifactory/v10",
			"github.com/jfrog/terraform-provider-artifactory",
		},
		{ // Multi-digit mod path
			"github.com/jfrog/terraform-provider-artifactory/v33",
			"github.com/jfrog/terraform-provider-artifactory",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			actual := modPathWithoutVersion(tt.input)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

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

func TestPullRequestTitle(t *testing.T) {
	t.Run("prefix", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "[TEST]", UpgradeBridgeVersion: true}
		bridgeVersion, err := ParseRef("v5.3.1")
		assert.Nil(t, err)
		got, err := prTitle(uc.Wrap(ctx), nil, bridgeVersion, nil)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})

	t.Run("no-prefix", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "", UpgradeBridgeVersion: true}
		bridgeVersion, err := ParseRef("v5.3.1")
		assert.Nil(t, err)
		got, err := prTitle(uc.Wrap(ctx), nil, bridgeVersion, nil)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})

	t.Run("provider-upgrade", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "", UpgradeProviderVersion: true, UpstreamProviderName: "terraform-provider-aws"}
		got, err := prTitle(uc.Wrap(ctx), &UpstreamUpgradeTarget{Version: semver.MustParse("5.3.0")}, nil, nil)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})
}

func TestGetExpectedTargetFromUpstream(t *testing.T) {
	repo := "pulumi/pulumi-cloudflare"

	testReplay((&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-cloudflare",
		UpstreamProviderOrg:  "cloudflare",
	}).Wrap(context.Background()), t, jsonMarshal[[]*step.Step](t, `[
  {
    "name": "Get Expected Target",
    "inputs": [
      "`+repo+`"
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
	"inputs": [],
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
]`), "Get Expected Target", getExpectedTarget)
}

func TestGetExpectedTargetFromTarget(t *testing.T) {
	repo := "pulumi/pulumi-cloudflare"
	test := func(testName string, inferVersion bool, targetVersion, expected string) {
		t.Run(testName, func(t *testing.T) {
			expectedSteps := jsonMarshal[[]*step.Step](t, expected)
			ctx := (&Context{
				GoPath:               "/Users/myuser/go",
				UpstreamProviderName: "terraform-provider-cloudflare",
				UpstreamProviderOrg:  "cloudflare",
				InferVersion:         inferVersion,
				TargetVersion:        semver.MustParse(targetVersion),
			}).Wrap(context.Background())
			testReplay(ctx, t, expectedSteps,
				"Get Expected Target", getExpectedTarget)
		})
	}

	test("expected-target", false, "4.19.0", `[
  {
    "name": "Get Expected Target",
    "inputs": [
      "`+repo+`"
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
      "` + repo + `"
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

func TestExpectedTargetLatest(t *testing.T) {
	ctx := (&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-akamai",
		UpstreamProviderOrg:  "akamai",
	}).Wrap(context.Background())

	testReplay(ctx, t, jsonMarshal[[]*step.Step](t, `[
	{
	  "name": "From Upstream Releases",
	  "inputs": [],
	  "outputs": [
		{
		  "Version": "5.5.0",
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
		  "--repo=akamai/terraform-provider-akamai",
		  "--exclude-drafts",
		  "--exclude-pre-releases"
		]
	  ],
	  "outputs": [
		"v5.5.0\tLatest\tv5.5.0\t2023-12-07T15:22:04Z\nv5.4.0\t\tv5.4.0\t2023-10-31T13:18:57Z\nv5.3.0\t\tv5.3.0\t2023-09-26T13:28:16Z\nv5.2.0\t\tv5.2.0\t2023-08-29T14:27:47Z\nv5.1.0\t\tv5.1.0\t2023-08-01T09:37:02Z\nv5.0.1\t\tv5.0.1\t2023-07-12T09:34:26Z\nv5.0.0\t\tv5.0.0\t2023-07-05T11:29:09Z\nv4.1.0\t\tv4.1.0\t2023-06-01T13:02:18Z\nv4.0.0\t\tv4.0.0\t2023-05-30T13:02:37Z\nv3.6.0\t\tv3.6.0\t2023-04-27T08:59:25Z\nv3.5.0\t\tv3.5.0\t2023-03-30T14:03:22Z\nv3.4.0\t\tv3.4.0\t2023-03-02T13:42:38Z\nv3.3.0\t\tv3.3.0\t2023-02-02T09:56:51Z\nv3.2.1\t\tv3.2.1\t2022-12-16T14:06:02Z\nv3.2.0\t\tv3.2.0\t2022-12-15T15:04:40Z\nv3.1.0\t\tv3.1.0\t2022-12-01T12:52:03Z\nv3.0.0\t\tv3.0.0\t2022-10-27T10:24:21Z\nv2.4.2\t\tv2.4.2\t2022-10-04T08:46:49Z\nv2.4.1\t\tv2.4.1\t2022-09-29T13:36:45Z\nv2.3.0\t\tv2.3.0\t2022-08-25T09:06:18Z\nv2.2.0\t\tv2.2.0\t2022-06-30T09:24:03Z\nv2.1.1\t\tv2.1.1\t2022-06-09T11:13:10Z\nv2.1.0\t\tv2.1.0\t2022-06-02T07:44:28Z\nv2.0.0\t\tv2.0.0\t2022-04-28T09:28:19Z\nv1.12.1\t\tv1.12.1\t2022-04-06T08:34:22Z\nv1.12.0\t\tv1.12.0\t2022-04-04T10:52:09Z\nv1.11.0\t\tv1.11.0\t2022-03-03T12:41:25Z\nv1.10.1\t\tv1.10.1\t2022-02-10T10:11:56Z\nv1.10.0\t\tv1.10.0\t2022-01-27T10:39:23Z\nv1.9.1\t\tv1.9.1\t2021-12-16T10:42:24Z\n",
		null
	  ],
	  "impure": true
	}
]`), "From Upstream Releases", getExpectedTargetLatest)
}

func TestFromUpstreamReleasesBetaIgnored(t *testing.T) {
	ctx := (&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-postgresql",
		UpstreamProviderOrg:  "cyrilgdn",
	}).Wrap(context.Background())

	testReplay(ctx, t, jsonMarshal[[]*step.Step](t, `[
	{
	  "name": "From Upstream Releases",
	  "inputs": [],
	  "outputs": [
		{
		  "Version": "1.21.0",
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
		  "--repo=cyrilgdn/terraform-provider-postgresql",
		  "--exclude-drafts",
		  "--exclude-pre-releases"
		]
	  ],
	  "outputs": [
		"v1.21.1-beta.1\tLatest\tv1.21.1-beta.1\t2023-11-01T15:46:02Z\nv1.21.0\t\tv1.21.0\t2023-09-10T15:47:25Z\nv1.20.0\t\tv1.20.0\t2023-07-14T15:40:36Z\nv1.19.0\t\tv1.19.0\t2023-03-18T21:39:45Z\nv1.18.0\t\tv1.18.0\t2022-11-26T12:41:47Z\nv1.17.1\t\tv1.17.1\t2022-08-19T18:11:52Z\nv1.17.0\t\tv1.17.0\t2022-08-19T17:11:00Z\nv1.16.0\t\tv1.16.0\t2022-05-08T14:47:45Z\nv1.15.0\t\tv1.15.0\t2022-02-04T16:39:44Z\nv1.14.0\t\tv1.14.0\t2021-08-22T13:58:27Z\nv1.13.0\t\tv1.13.0\t2021-05-21T08:56:31Z\nv1.12.1\t\tv1.12.1\t2021-04-23T12:47:59Z\nv1.13.0-pre1\t\tv1.13.0-pre1\t2021-04-23T12:45:27Z\nv1.12.0\t\tv1.12.0\t2021-03-26T08:39:45Z\nv1.11.2\t\tv1.11.2\t2021-02-16T18:54:47Z\nv1.11.1\t\tv1.11.1\t2021-02-02T21:55:14Z\nv1.11.0\t\tv1.11.0\t2021-01-10T17:08:43Z\nv1.11.0-pre-gocloud\t\tv1.11.0-pre-gocloud\t2021-01-03T15:09:39Z\nv1.10.0\t\tv1.10.0\t2021-01-02T15:25:08Z\nv1.9.0\t\tv1.9.0\t2020-12-21T19:42:22Z\nv1.8.1\t\tv1.8.1\t2020-11-26T14:52:38Z\nv1.8.0\t\tv1.8.0\t2020-11-26T13:05:53Z\nv1.7.2\t\tv1.7.2\t2020-07-30T21:22:38Z\n",
		null
	  ],
	  "impure": true
	}
]`), "From Upstream Releases", getExpectedTargetLatest)
}
