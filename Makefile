bin:
	mkdir -p bin

build: tidy bin
	go build -o ./bin/upgrade-provider github.com/pulumi/upgrade-provider

tidy:
	go mod tidy

install:
	go install github.com/pulumi/upgrade-provider

lint:
	golangci-lint run -E gci --fix

test:
	go test -v ./...
