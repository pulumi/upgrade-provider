
build: tidy
	go build github.com/pulumi/upgrade-provider

tidy:
	go mod tidy
