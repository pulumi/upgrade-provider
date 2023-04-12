package migrations

import (
	"testing"
)

func TestAddAutoAliasingSourceCode(t *testing.T) {
	orig := `package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mrparkers/terraform-test/provider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
)

// all of the token components used below.
const (
	// packages:
	mainPkg = "test"
	// modules:
	mainMod           = "index"          // the y module
)

// Provider returns additional overlaid schema and metadata associated with the provider..
func Provider() tfbridge.ProviderInfo {
	// Instantiate the Terraform provider
	p := shimv2.NewProvider(provider.KeycloakProvider(nil))

	// Create a Pulumi provider mapping
	prov := tfbridge.ProviderInfo{
		P:                 p,
		Name:              "test",
		GitHubOrg:         "testing",
		Description:       "A Pulumi package for creating and managing test cloud resources.",
		Keywords:          []string{"pulumi", "test"},
		License:           "Apache-2.0",
		Homepage:          "https://pulumi.io",
		Repository:        "https://github.com/pulumi/pulumi-keycloak",
		TFProviderLicense: refProviderLicense(tfbridge.MITLicenseType),
		UpstreamRepoPath:  ".",

	prov.SetAutonaming(255, "-")

	return prov
}
`
	fs.token.NewFileSet()
	f, err := parser.Parse(fs, "test", []byte(orig), parser.DeclarationErrors|parser.Comments)
	contract.AssertNoErrorf(err, "failed to parse file")
	out := []byte("")
	// convert byte slice to io.Reader
	reader := bytes.NewReader(out)
	AddAutoAliasingSourceCode(fs, f, reader)

	expected := `package test

import (
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	// "embed package not used directly"
	"_ embed"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mrparkers/terraform-test/provider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
)

"go:embed cmd/pulumi-resource-databricks/bridge-metadata.json"
var metadata []byte

// all of the token components used below.
const (
	// packages:
	mainPkg = "test"
	// modules:
	mainMod           = "index"          // the y module
)

// Provider returns additional overlaid schema and metadata associated with the provider..
func Provider() tfbridge.ProviderInfo {
	// Instantiate the Terraform provider
	p := shimv2.NewProvider(provider.KeycloakProvider(nil))

	// Create a Pulumi provider mapping
	prov := tfbridge.ProviderInfo{
		P:                 p,
		Name:              "test",
		GitHubOrg:         "testing",
		Description:       "A Pulumi package for creating and managing test cloud resources.",
		Keywords:          []string{"pulumi", "test"},
		License:           "Apache-2.0",
		Homepage:          "https://pulumi.io",
		Repository:        "https://github.com/pulumi/pulumi-keycloak",
		TFProviderLicense: refProviderLicense(tfbridge.MITLicenseType),
		UpstreamRepoPath:  ".",
		MetadataInfo:      tfbridge.NewProviderMetadata(metadata),

	err = x.AutoAliasing(&prov, prov.GetMetadata())
	contract.AssertNoErrorf(err, "auto aliasing failed")
	prov.SetAutonaming(255, "-")

	return prov
}
`
	// assert.Equal(t, writer.String(), expected)

}
