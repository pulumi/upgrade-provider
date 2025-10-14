# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This repository contains the `upgrade-provider` tool, which automates the process of upgrading bridged Pulumi providers. The tool reduces manual intervention in provider upgrades by automating bridge updates, upstream provider version upgrades, and SDK generation.

## Build and Development Commands

- **Build**: `make build` - Compiles the binary to `./bin/upgrade-provider`
- **Install**: `make install` - Installs the tool using `go install`
- **Test**: `make test` - Runs all tests with `go test -v ./...`
- **Lint**: `make lint` - Runs golangci-lint
- **Lint Fix**: `make lint.fix` - Runs golangci-lint with auto-fix
- **Tidy**: `make tidy` - Runs `go mod tidy`

## Code Architecture

The codebase is organized around a pipeline-based architecture using the `step` library:

### Core Components

- **main.go**: CLI entry point using Cobra for command-line interface and Viper for configuration management
- **upgrade/**: Contains the core upgrade logic and pipeline definitions
  - `upgrade_provider.go`: Main upgrade orchestration using stepv2 pipelines
  - `steps.go`: Defines individual upgrade steps and pipeline execution
  - `versions.go`: Handles version resolution and comparison logic
- **step/**: Pipeline execution library with two versions:
  - `step.go`: Original step library with spinner-based UI
  - `step/v2/`: Enhanced step library with improved error handling and replay capabilities

### Key Architecture Patterns

- **Pipeline Architecture**: Operations are structured as pipelines of atomic steps that can be displayed to users and halt on errors
- **Context-Driven Execution**: Uses Go context for configuration passing and cancellation
- **Step-Based Execution**: Each operation is broken down into displayable, atomic steps with spinner UI feedback
- **Replay Testing**: stepv2 supports recording and replaying operations for testing using `PULUMI_REPLAY=logs.json`

### Configuration

- Configuration via `.upgrade-config.yml` in provider repository root
- Environment variables prefixed with `UPGRADE_` (e.g., `UPGRADE_UPSTREAM_PROVIDER_NAME`)
- CLI flags with kebab-case names that map to environment variables with underscores

### Upgrade Process Flow

The tool follows a structured upgrade process:
1. Repository discovery and validation
2. Version determination and target resolution
3. Branch creation and checkout
4. Dependency upgrades (bridge, upstream provider)
5. Code generation (`make tfgen`, `make generate_sdks`)
6. Git operations and PR creation

## Testing

- Unit tests use the standard Go testing framework with testify
- Integration tests can use replay functionality with `PULUMI_REPLAY=logs.json`
- Golden file testing with hexops/autogold for output verification