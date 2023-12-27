package upgrade

var ProviderOrgs = map[string]string{
	"datadog":  "DataDog",
	"random":   "hashicorp",
	"archive":  "terraform-providers",
	"external": "terraform-providers",
	"local":    "terraform-providers",
}

var ProviderName = map[string]string{
	"f5bigip":        "bigip",
	"confluentcloud": "confluent",
}
