module github.com/pulumi/upgrade-provider/step/v2/examples/simple

go 1.23

toolchain go1.23.4

replace github.com/pulumi/upgrade-provider => ../../../../

require (
	github.com/pulumi/upgrade-provider v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.8.3
)

require (
	github.com/briandowns/spinner v1.20.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fatih/color v1.15.0 // indirect
	github.com/golang/glog v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/pulumi/pulumi/sdk/v3 v3.87.0 // indirect
	golang.org/x/sys v0.20.0 // indirect
	golang.org/x/term v0.11.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
