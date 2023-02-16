
build: tidy
	go build github.com/pulumi/upgrade-provider

tidy:
	go mod tidy

install:
	go install github.com/pulumi/upgrade-provider

lint:
	golangci-lint run

test:
	go test ./...
