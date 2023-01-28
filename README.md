# upgrade-provider

This repo contains the `upgrade-provider` tool. `upgrade-provider` aims to reduce the
amount of human intervention necessary for upgrading providers to a limit of zero.

Realistically, we can reduce provider upgrades to 3 operations:

- resolving merge dependencies for forked providers
- manual provider mappings in `resources.go`
- resolving build conflicts on updates

## Usage

`upgrade-provider` relies on the command line utilities `gh` and `git` to run, as well as
all tools necessary for a manual provider upgrade. That generally means `make` and `go`.

`upgrade-provider` takes exactly one option: the provider to upgrade. This corresponds to

A typical run for a forked provider will look like this:

```
$ ./upgrade-provider pulumi-fastly
---- Discovering Repository ----
✓ Getting Repo: /Users/ianwahbe/go/src/github.com/pulumi/pulumi-fastly
✓ Set default branch: master
✓ Upgrade version: 3.0.4
✓ Repo kind: forked
---- Upgrading Forked Provider ----
✓ Ensure upstream repo: /Users/ianwahbe/go/src/github.com/fastly/terraform-provider-fastly
✓ Ensure pulumi remote: 'pulumi' already exists
✓ /usr/local/bin/git fetch pulumi: done
✓ Discover previous upstream version: 3.0.4
✓ checkout upstream: done
✓ upstream branch: upstream-v3.0.4 already exists
✓ merge upstream branch: no conflict
✓ /usr/local/bin/go build .: done
✓ push upstream: done
✓ get head commit: 27251e78c400e684bae5225c5394b85743ebf28b
---- Upgrading Provider ----
✓ ensure branch: switching to upgrade-terraform-provider-fastly-to-v3.0.4
✓ /usr/local/bin/go get -u github.com/pulumi/pulumi-terraform-bridge/v3: done
✓ /usr/local/bin/go get github.com/fastly/terraform-provider-fastly@v3.0.4: done
✓ /usr/local/bin/go mod edit -replace github.com/fastly/terraform-provider-fastly=github.com/pulumi/terraform-provider-fastly@27251e78c400e684bae5225c5394b85743ebf28b: done
✓ /usr/local/bin/go mod tidy: done
✓ /usr/bin/make tfgen: done
✓ /usr/local/bin/git add --all: done
✓ /usr/local/bin/git commit -m make tfgen: done
✓ /usr/bin/make build_sdks: done
✓ /usr/local/bin/git add --all: done
✓ /usr/local/bin/git commit -m make build_sdks: done
```

## How it works

`upgrade-provider` executes the same process to upgrade a pulumi provider as a manual upgrade. The basic pipeline goes as follows:

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
