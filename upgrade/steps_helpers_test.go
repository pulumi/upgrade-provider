package upgrade

import (
	"context"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/hexops/autogold/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"
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

func TestPullRequestBody(t *testing.T) {
	t.Run("description-space", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("description-equal", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description=" + uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("upgrades", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{
			UpgradeBridgeVersion: true,
		}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, &GoMod{
			Bridge: module.Version{Version: "v1.2.2"},
		},
			&Version{SemVer: semver.MustParse("v1.2.3")}, "", args)
		autogold.ExpectFile(t, got)
	})
}

func TestPullRequestTitle(t *testing.T) {
	t.Run("prefix", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "[TEST]", UpgradeBridgeVersion: true}
		bridgeVersion, err := ParseRef("v5.3.1")
		assert.Nil(t, err)
		got, err := prTitle(uc.Wrap(ctx), nil, bridgeVersion)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})

	t.Run("no-prefix", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "", UpgradeBridgeVersion: true}
		bridgeVersion, err := ParseRef("v5.3.1")
		assert.Nil(t, err)
		got, err := prTitle(uc.Wrap(ctx), nil, bridgeVersion)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})

	t.Run("provider-upgrade", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRTitlePrefix: "", UpgradeProviderVersion: true, UpstreamProviderName: "terraform-provider-aws"}
		got, err := prTitle(uc.Wrap(ctx), &UpstreamUpgradeTarget{Version: semver.MustParse("5.3.0")}, nil)
		assert.Nil(t, err)
		autogold.ExpectFile(t, got)
	})
}

func TestExpectedTargetLatest(t *testing.T) {
	testRunner := &TestRunner{}
	ctx := (&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-akamai",
		UpstreamProviderOrg:  "akamai",
		r:                    testRunner,
	}).Wrap(context.Background())

	testRunner.mockMap = map[string]RunResult{
		"gh release list --repo=akamai/terraform-provider-akamai --exclude-drafts --exclude-pre-releases": {
			Output: "v5.5.0\tLatest\tv5.5.0\t2023-12-07T15:22:04Z\nv5.4.0\t\tv5.4.0\t2023-10-31T13:18:57Z\nv5.3.0\t\tv5.3.0\t2023-09-26T13:28:16Z\nv5.2.0\t\tv5.2.0\t2023-08-29T14:27:47Z\nv5.1.0\t\tv5.1.0\t2023-08-01T09:37:02Z\nv5.0.1\t\tv5.0.1\t2023-07-12T09:34:26Z\nv5.0.0\t\tv5.0.0\t2023-07-05T11:29:09Z\nv4.1.0\t\tv4.1.0\t2023-06-01T13:02:18Z\nv4.0.0\t\tv4.0.0\t2023-05-30T13:02:37Z\nv3.6.0\t\tv3.6.0\t2023-04-27T08:59:25Z\nv3.5.0\t\tv3.5.0\t2023-03-30T14:03:22Z\nv3.4.0\t\tv3.4.0\t2023-03-02T13:42:38Z\nv3.3.0\t\tv3.3.0\t2023-02-02T09:56:51Z\nv3.2.1\t\tv3.2.1\t2022-12-16T14:06:02Z\nv3.2.0\t\tv3.2.0\t2022-12-15T15:04:40Z\nv3.1.0\t\tv3.1.0\t2022-12-01T12:52:03Z\nv3.0.0\t\tv3.0.0\t2022-10-27T10:24:21Z\nv2.4.2\t\tv2.4.2\t2022-10-04T08:46:49Z\nv2.4.1\t\tv2.4.1\t2022-09-29T13:36:45Z\nv2.3.0\t\tv2.3.0\t2022-08-25T09:06:18Z\nv2.2.0\t\tv2.2.0\t2022-06-30T09:24:03Z\nv2.1.1\t\tv2.1.1\t2022-06-09T11:13:10Z\nv2.1.0\t\tv2.1.0\t2022-06-02T07:44:28Z\nv2.0.0\t\tv2.0.0\t2022-04-28T09:28:19Z\nv1.12.1\t\tv1.12.1\t2022-04-06T08:34:22Z\nv1.12.0\t\tv1.12.0\t2022-04-04T10:52:09Z\nv1.11.0\t\tv1.11.0\t2022-03-03T12:41:25Z\nv1.10.1\t\tv1.10.1\t2022-02-10T10:11:56Z\nv1.10.0\t\tv1.10.0\t2022-01-27T10:39:23Z\nv1.9.1\t\tv1.9.1\t2021-12-16T10:42:24Z\n",
		},
	}

	expectedTargetLatest, err := getExpectedTargetLatest(ctx)
	require.NoError(t, err)
	require.Equal(t, "5.5.0", expectedTargetLatest.Version.String())
	require.Equal(t, 0, len(expectedTargetLatest.GHIssues))
}

func TestFromUpstreamReleasesBetaIgnored(t *testing.T) {
	testRunner := &TestRunner{}
	ctx := (&Context{
		GoPath:               "/Users/myuser/go",
		UpstreamProviderName: "terraform-provider-postgresql",
		UpstreamProviderOrg:  "cyrilgdn",
		r:                    testRunner,
	}).Wrap(context.Background())
	testRunner.mockMap = map[string]RunResult{
		"gh release list --repo=cyrilgdn/terraform-provider-postgresql --exclude-drafts --exclude-pre-releases": {
			Output: "v1.21.1-beta.1\tLatest\tv1.21.1-beta.1\t2023-11-01T15:46:02Z\nv1.21.0\t\tv1.21.0\t2023-09-10T15:47:25Z\nv1.20.0\t\tv1.20.0\t2023-07-14T15:40:36Z\nv1.19.0\t\tv1.19.0\t2023-03-18T21:39:45Z\nv1.18.0\t\tv1.18.0\t2022-11-26T12:41:47Z\nv1.17.1\t\tv1.17.1\t2022-08-19T18:11:52Z\nv1.17.0\t\tv1.17.0\t2022-08-19T17:11:00Z\nv1.16.0\t\tv1.16.0\t2022-05-08T14:47:45Z\nv1.15.0\t\tv1.15.0\t2022-02-04T16:39:44Z\nv1.14.0\t\tv1.14.0\t2021-08-22T13:58:27Z\nv1.13.0\t\tv1.13.0\t2021-05-21T08:56:31Z\nv1.12.1\t\tv1.12.1\t2021-04-23T12:47:59Z\nv1.13.0-pre1\t\tv1.13.0-pre1\t2021-04-23T12:45:27Z\nv1.12.0\t\tv1.12.0\t2021-03-26T08:39:45Z\nv1.11.2\t\tv1.11.2\t2021-02-16T18:54:47Z\nv1.11.1\t\tv1.11.1\t2021-02-02T21:55:14Z\nv1.11.0\t\tv1.11.0\t2021-01-10T17:08:43Z\nv1.11.0-pre-gocloud\t\tv1.11.0-pre-gocloud\t2021-01-03T15:09:39Z\nv1.10.0\t\tv1.10.0\t2021-01-02T15:25:08Z\nv1.9.0\t\tv1.9.0\t2020-12-21T19:42:22Z\nv1.8.1\t\tv1.8.1\t2020-11-26T14:52:38Z\nv1.8.0\t\tv1.8.0\t2020-11-26T13:05:53Z\nv1.7.2\t\tv1.7.2\t2020-07-30T21:22:38Z\n",
		},
	}

	expectedTargetLatest, err := getExpectedTargetLatest(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.21.0", expectedTargetLatest.Version.String())
	require.Equal(t, 0, len(expectedTargetLatest.GHIssues))
}
