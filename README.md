# upgrade-provider

This repo contains the `upgrade-provider` tool. `upgrade-provider` aims to reduce the
amount of human intervention necessary for upgrading providers to a limit of zero.

With our current bridged provider structure, we can reduce provider upgrades to 3 manual
operations:

- resolving merge dependencies for forked providers
- manual provider mappings in `resources.go`
- resolving build conflicts on updates

The rest of an upgrade is formulaic, and can thus be automated. This tool attempts to do
the rest.

## Installation

We don't currently maintain releases for this tool. To install this tool onto your path,
clone the repository and run `make install`.

## Requirements
- Go version `1.20`
- `git` version `>=2.36.0`

## Usage

`upgrade-provider` relies on the command line utilities `gh` and `git` to run, as well as
all tools necessary for a manual provider upgrade. That generally means `pulumi`, `make`
and the build toolchain for each released SDK.

`upgrade-provider` takes exactly one option: the name of the Pulumi repository to upgrade.
There is not setup necessary, nor is it required to have local copies of

A typical run for a forked provider will look like this:

```
❯ ./upgrade-provider pulumi-snowflake
---- Discovering Repository ----
- Ensure 'github.com/pulumi/pulumi-snowflake'
  - ✓ Expected Location: /Users/ianwahbe/go/src/github.com/pulumi/pulumi-snowflake
  - ✓ Downloading: done
  - ✓ Validating: /Users/ianwahbe/go/src/github.com/pulumi/pulumi-snowflake
- pull default branch
  - ✓ /usr/local/bin/git ls-remote --heads origin: done
  - ✓ finding default branch: master
  - ✓ /usr/local/bin/git checkout master: done
  - ✓ /usr/local/bin/git pull origin: done
- ✓ Upgrade version: 0.56.3
- ✓ Repo kind: plain
---- Upgrading Provider ----
- Ensure Branch
  - ✓ /usr/local/bin/git branch: done
  - ✓ Already exists: no
  - ✓ /usr/local/bin/git checkout -b upgrade-terraform-provider-snowflake-to-v0.56.3: done
  - ✓ /usr/local/bin/git checkout upgrade-terraform-provider-snowflake-to-v0.56.3: done
- ✓ /usr/local/bin/go get -u github.com/pulumi/pulumi-terraform-bridge/v3: done
- ✓ Lookup Tag SHA: 9c69643a31d91d0f3d249f7aea3beeefc53880ae
- ✓ /usr/local/bin/go get github.com/Snowflake-Labs/terraform-provider-snowflake@9c6...: done
- ✓ /usr/local/bin/go mod tidy: done
- ✓ /Users/ianwahbe/go/bin/pulumi plugin rm --all --yes: done
- ✓ /usr/bin/make tfgen: done
- ✓ /usr/local/bin/git add --all: done
- ✓ /usr/local/bin/git commit -m make tfgen: done
- ✓ /usr/bin/make build_sdks: done
- ✓ /usr/local/bin/git add --all: done
- ✓ /usr/local/bin/git commit -m make build_sdks: done
- Open PR
  - ✓ /usr/local/bin/git push --set-upstream origin upgrade-terraform-provider-snowfla...: done
  - ✓ /usr/local/bin/gh pr create --assignee @me --base master --head upgrade-terrafor...: done
  - Self Assign Issues
    - ✓ /usr/local/bin/gh issue edit 183 --add-assignee @me: done
    - ✓ /usr/local/bin/gh issue edit 182 --add-assignee @me: done
    - ✓ /usr/local/bin/gh issue edit 181 --add-assignee @me: done
```

If the process succeeds, you can go to GH and open a new PR. All local work is done.

### Dealing with manual steps

The process will fail where manual intervention is required. The failed step will have a
useful error message that should tell you how to address the problem. If a subprocess like
`git` or `make` fails, its output will be printed.

To fix an error:

- Go to the provider repo and ensure that you are on upgrade branch with no commits (changes are ok).
- Make the necessary changes in repo. Do not commit.
- Re-run the tool. It will include your changes on the next attempt.

Repeat as necessary for a working upgrade.

## How it works

`upgrade-provider` defines pipelines, where a pipeline is a set of synchronous and ordered
steps. This leverages the `step` module in the repo. The hope is that each pipeline can be
self-documenting. This is the pipeline for running an upgrade after any `go.mod` fixes are
already applies:

```go
ok = step.Run(step.Combined("Upgrading Provider",
		append(steps,
			step.Cmd(exec.CommandContext(ctx, "go", "mod", "tidy")).In(&providerPath),
			step.Cmd(exec.CommandContext(ctx, "pulumi", "plugin", "rm", "--all", "--yes")),
			step.Cmd(exec.CommandContext(ctx, "make", "tfgen")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make tfgen")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "make", "build_sdks")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "add", "--all")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "commit", "-m", "make build_sdks")).In(&path),
			step.Cmd(exec.CommandContext(ctx, "git", "push", "--set-upstream", "origin", branchName)).In(&path),
		)...))
```

### Existing Pipelines

`upgrade-provider` executes the same process to upgrade a Pulumi provider as a manual
upgrade. The basic pipeline goes as follows:

1. Download the pulumi provider repository if its not present.
2. Check with github to get the version to upgrade to.
3. Determine the type of upgrade to perform (forked or normal).

If the provider references a pulumi owned fork in it's provider/go.mod:

1. Download the fork to it's appropriate place on the file system (if not present).
2. Checkout a new branch upstream of the target version and then merge the previous
   upstream into it.
3. Check that we can still build cleanly
4. Push the upstream branch back into the pulumi fork.

Then the basic provider upgrade is performed:

1. Checkout the repo, pull the latest master, and create a new branch for the upgrade.
2. Upgrade the upstream dependency. (If forked, update the fork)
3. Upgrade terraform bridge.
4. Run `make tfgen` and check in the result.
5. Run `make build_sdks` and check in the result.

If `shim` is a subfolder of `provider`, then upgrades will be performed in `shim`.

## Configuration

A configuration file `.upgrade-config.{yml/json}` may be defined within the provider directory.
Values include:
- `upstream-provider-name`: The name of the upstream provider repo, i.e. `terraform-provider-docker`
- `experimental`: Whether to enable experimental `pulumi-terraform-bridge` features https://github.com/pulumi/pulumi-terraform-bridge/tree/master/pkg/tfbridge/x. Value must be [true, false].


## Project Guidelines

### Goals

- Automate the boring stuff. If a task is simple enough for a computer to do, then we
  should let the computer do it.
- Treat all upgrades the same. Upgrading a provider shouldn't have different steps
  depending on if we maintain an upstream fork.
- Easy to understand. `upgrade-provider` should inform the user what it's doing. If
  something breaks, the user should be able to diagnose and complte the process on their
  own.
- Idempotent. It should always be safe to run `upgrade-provider`, regardless of where the
  user is in an upgrade.

### Non-goals

- Heuristic processes. `upgrade-provider` should make no effort to solve ambiguous
  problems, such as build conflicts. If the next step isn't obvious, fail over to the
  human operator.
