# Updating bridged providers to a new major version

This guide describes the process of upgrading a bridged Pulumi provider to a new major version.

**Note:** None of these steps are necessary or appropriate when moving from version 0.x to 1.0 because Go modules only
require a version suffix for versions >= 2.0.

For the purposes of example code in this document, we'll assume the provider is being upgraded from v1 to v2.

## Updating the provider code for the new version

In the provider repo:

1. Set the `major-version` field in `.ci-mgmt.yaml` to `2`:

    ```patch
    -major-version: 1
    +major-version: 2
    ```

1. Run `make ci-mgmt` to reflect the changes made in the previous step. At minimum, you
   should see these changes in `.goreleaser.prerelease.yml`, `.goreleaser.yml` and
   `Makefile`:

    .goreleaser.prerelease.yml, .goreleaser.yml:

    ```patch
    ldflags:
    -- -X github.com/pulumi/pulumi-${PROVIDER}/provider/pkg/version.Version={{.Tag}}
    +- -X github.com/pulumi/pulumi-${PROVIDER}/provider/v2/pkg/version.Version={{.Tag}}
    ```

    Makefile:

    ```patch
    -PROVIDER_PATH := provider
    +PROVIDER_PATH := provider/v2
    ```

1. In `provider/go.mod`, change the `module` directive to be v2, e.g.:

    ```go
    module github.com/pulumi/pulumi-${PROVIDER}/provider/v2
    ```

1. Download the updated dependencies for the provider:

    ```bash
    cd provider && go mod tidy
    ```

1. Change all version references to the new version in the codebase, e.g in
   `provider/cmd/pulumi-resource-${PROVIDER}/main.go`:

    ```go
    import (
      "github.com/pulumi/pulumi-${PROVIDER}/provider/v${NEXT_VERSION}"
      "github.com/pulumi/pulumi-${PROVIDER}/provider/v${NEXT_VERSION}/pkg/version"
    )
    ```

    Ensure that in `provider/resources.go` in `tfbridge.ProviderInfo` the field `TFProviderModuleVersion` is updated.

1. Ensure that Go code under `examples` is updated.

   This also includes updating `Dependencies` to point to the new version of the SDK.

   ```go
   baseGo := base.With(integration.ProgramTestOptions{
       Dependencies: []string{
           "github.com/pulumi/pulumi-${PROVIDER}/sdk/v2",
       },
   })
   ```

1. Compile the code:

    ```bash
    VERSION_PREFIX=2.0.0 make tfgen
    ```

    `VERSION_PREFIX` instructs `pulumictl` tool to compute 2.x.x versions before any tags are applied, which is
    important because the generated Go code under sdk/go is sensitive to the major version.

1. Build the SDKs for the new version:

    ```bash
    VERSION_PREFIX=2.0.0 make build_sdks
    ```

1. In `sdk/go.mod`, update the `module` directive to the new version

1. Download the updated dependencies for the SDK:

    ```bash
    cd sdk && go mod tidy
    ```

1. In `README.md`, update all instructions to reference the new version of the provider, including updating the version
   of the Go SDK.

1. Now `git commit` the changes and tag them with `git tag v2.0.0-alpha.0`. Note that the tag will make sure that
   `pulumictl` computes the right version both locally and in CI, and making it `-alpha` makes sure the changes do not
   go out as a production release before they are ready.

1. Create a PR for the provider upgrade. Also, push the new tag so the remote is aware of it:
   `git push origin v2.0.0-alpha.0`.

   You may need to iterate to update examples for any breaking changes.

   When merging the PR, make sure to use "Create a merge commit" method (and not Rebase or Squash) so that after merging
   to the main branch the tag continues pointing to a valid commit on the main branch.


## Notes

Previous versions of this playbook recommended skipping acceptance tests. This is not actually inevitable and is
considered harmful as it can lead to forgotten deferred work post-merge like unskipping the tests or making them comply
with breaking changes.
