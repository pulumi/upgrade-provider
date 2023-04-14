package upgrade

import (
	"io/ioutil"
	"os"
	"path/filepath"
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
	tmpDir := t.TempDir()
	origPath := filepath.Join(tmpDir, "original.go")
	orig, err := os.Create(origPath)
	assert.Nil(t, err)
	_, err = orig.Write([]byte(origProgram))
	assert.Nil(t, err)

	// Perform auto aliasing migration
	err = AutoAliasingMigration(origPath, "test")
	assert.Nil(t, err)

	modified, err := ioutil.ReadFile(origPath)
	assert.Nil(t, err)

	expected := `package test

import (
	"fmt"
	// embed is used to store bridge-metadata.json in the compiled binary
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
	assert.Equal(t, string(modified), expected)

	// Test running AutoAliasing twice doesn't change output
	err = AutoAliasingMigration(origPath, "test")
	assert.Nil(t, err)

	modified2, err := ioutil.ReadFile(origPath)
	assert.Equal(t, string(modified2), expected)
}
