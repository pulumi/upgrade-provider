package upgrade

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/mod/module"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestGetUpstreamProviderOrgFromModfile(t *testing.T) {

	upstreamVersion := module.Version{Path: "github.com/testing-org/terraform-provider-datadog", Version: "v0.0.0"}

	simpleReplay(t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Get UpstreamOrg",
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
		result := getUpstreamProviderOrg(context.Wrap(ctx), upstreamVersion)
		assert.NotNil(t, result)
		assert.Equal(t, result, "testing-org")
	})
}
