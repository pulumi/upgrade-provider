package upgrade

import (
	"go/parser"
	"go/token"
	"ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutoAliasingMigration(t *testing.T) {
	origProgram := `package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mrparkers/terraform-test/provider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
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
	}
	prov.SetAutonaming(255, "-")

	return prov
}
`

	// Write original program to temporary file
	orig, err := os.Create("original.go")
	assert.Nil(t, err)
	defer os.Remove("original.go")
	_, err = orig.Write([]byte(origProgram))
	assert.Nil(t, err)

	// Parse to ast
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "original.go", nil, parser.DeclarationErrors|parser.ParseComments)
	assert.Nil(t, err)

	// Perform auto aliasing migration
	_, err = AutoAliasingMigration(fs, f, "original.go", "test")
	assert.Nil(t, err)

	newProgram, err := ioutil.ReadFile("original.go")
	assert.Nil(t, err)

	expectedProgram := `package test

import (
	"fmt"
	// embed package blank import
	_ "embed"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mrparkers/terraform-test/provider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/x"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

// all of the token components used below.
const (
	// packages:
	mainPkg = "test"
	// modules:
	mainMod = "index" // the y module
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
		UpstreamRepoPath:  ".", MetadataInfo: tfbridge.NewProviderMetadata(metadata),
	}
	err := x.AutoAliasing(&prov, prov.GetMetadata())
	contract.AssertNoErrorf(err, "auto aliasing apply failed")
	prov.SetAutonaming(255, "-")

	return prov
}

//go:embed cmd/pulumi-resource-test/bridge-metadata.json
var metadata []byte
`
	// Compare against expected program
	assert.Equal(t, newProgram, []byte(expectedProgram))

}
