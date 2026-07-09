VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
COMMIT := $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

bin:
	mkdir -p bin

build: tidy bin
	go build -ldflags "$(LDFLAGS)" -o ./bin/upgrade-provider github.com/pulumi/upgrade-provider

tidy:
	go mod tidy

install:
	go install -ldflags "$(LDFLAGS)" github.com/pulumi/upgrade-provider

lint:
	golangci-lint run

lint.fix:
	golangci-lint run --fix

test:
	go test -v ./...
