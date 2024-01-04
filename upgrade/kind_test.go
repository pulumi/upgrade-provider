package upgrade

import (
	"context"
	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/assert"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"testing"
)

func TestGetUpstreamProviderOrgFromConfig(t *testing.T) {
	upstreamRequires := &modfile.Require{
		Mod: module.Version{Path: "github.com/terraform-providers/terraform-provider-datadog", Version: "v0.0.0"},
		Syntax: &modfile.Line{
			Start:   modfile.Position{Line: 9, LineRune: 2, Byte: 222},
			Token:   []string{"github.com/terraform-providers/terraform-provider-datadog", "v0.0.0"},
			InBlock: true,
			End:     modfile.Position{Line: 9, LineRune: 66, Byte: 286},
		},
	}

	simpleReplay(t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Get UpstreamOrg",
          "inputs": [
            {
              "Mod": {
                "Path": "github.com/terraform-providers/terraform-provider-datadog",
                "Version": "v0.0.0"
              },
              "Indirect": false,
              "Syntax": {
                "Before": null,
                "Suffix": null,
                "After": null,
                "Start": {
                  "Line": 9,
                  "LineRune": 2,
                  "Byte": 222
                },
                "Token": [
                  "github.com/terraform-providers/terraform-provider-datadog",
                  "v0.0.0"
                ],
                "InBlock": true,
                "End": {
                  "Line": 9,
                  "LineRune": 66,
                  "Byte": 286
                }
              }
            }
          ],
          "outputs": [
            "DataDog",
            null
          ]
        }
]`), func(ctx context.Context) {
		context := &Context{
			GoPath:               "/Users/myuser/go",
			UpstreamProviderName: "terraform-provider-datadog",
			UpstreamProviderOrg:  "DataDog",
		}
		result := getUpstreamProviderOrg(context.Wrap(ctx),
			upstreamRequires)
		assert.NotNil(t, result)
	})
}

func TestGetUpstreamProviderOrgFromModfile(t *testing.T) {
	upstreamRequires := &modfile.Require{
		Mod: module.Version{Path: "github.com/terraform-providers/terraform-provider-datadog", Version: "v0.0.0"},
		Syntax: &modfile.Line{
			Start:   modfile.Position{Line: 9, LineRune: 2, Byte: 222},
			Token:   []string{"github.com/terraform-providers/terraform-provider-datadog", "v0.0.0"},
			InBlock: true,
			End:     modfile.Position{Line: 9, LineRune: 66, Byte: 286},
		},
	}

	simpleReplay(t, jsonMarshal[[]*step.Step](t, `[
	{
          "name": "Get UpstreamOrg",
          "inputs": [
            {
              "Mod": {
                "Path": "github.com/terraform-providers/terraform-provider-datadog",
                "Version": "v0.0.0"
              },
              "Indirect": false,
              "Syntax": {
                "Before": null,
                "Suffix": null,
                "After": null,
                "Start": {
                  "Line": 9,
                  "LineRune": 2,
                  "Byte": 222
                },
                "Token": [
                  "github.com/terraform-providers/terraform-provider-datadog",
                  "v0.0.0"
                ],
                "InBlock": true,
                "End": {
                  "Line": 9,
                  "LineRune": 66,
                  "Byte": 286
                }
              }
            }
          ],
          "outputs": [
            "terraform-providers",
            null
          ]
        }
]`), func(ctx context.Context) {
		context := &Context{
			GoPath:               "/Users/myuser/go",
			UpstreamProviderName: "terraform-provider-datadog",
			UpstreamProviderOrg:  "",
		}
		result := getUpstreamProviderOrg(context.Wrap(ctx),
			upstreamRequires)
		assert.NotNil(t, result)
	})
}
