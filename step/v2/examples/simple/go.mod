module github.com/pulumi/upgrade-provider/step/v2/examples/simple

go 1.21.0

replace github.com/pulumi/upgrade-provider => ../../../../

require github.com/pulumi/upgrade-provider v0.0.0-00010101000000-000000000000

require (
	github.com/briandowns/spinner v1.20.0 // indirect
	github.com/fatih/color v1.14.1 // indirect
	github.com/golang/glog v1.1.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.18 // indirect
	github.com/pulumi/pulumi/sdk/v3 v3.87.0 // indirect
	golang.org/x/sys v0.9.0 // indirect
	golang.org/x/term v0.8.0 // indirect
)
