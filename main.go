package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"os"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/spf13/cobra"

	"github.com/pulumi/upgrade-provider/upgrade"
)

func cmd() *cobra.Command {
	var maxVersion string
	gopath, ok := os.LookupEnv("GOPATH")
	if !ok {
		gopath = build.Default.GOPATH
	}
	var upgradeKind string

	context := upgrade.Context{
		Context: context.Background(),
		GoPath:  gopath,
	}

	exitOnError := func(err error) {
		if err == nil {
			return
		}
		if !errors.Is(err, upgrade.ErrHandled) {
			fmt.Printf("error: %s\n", err.Error())
		}
		os.Exit(1)
	}

	cmd := &cobra.Command{
		Use:   "upgrade-provider",
		Short: "upgrade-provider automatics the process of upgrading a TF-bridged provider",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			// Validate that maxVersion is a valid version
			var err error
			if maxVersion != "" {
				context.MaxVersion, err = semver.NewVersion(maxVersion)
				if err != nil {
					return fmt.Errorf("--provider-version=%s: %w",
						maxVersion, err)
				}
			}

			// Validate the kind switch
			switch upgradeKind {
			case "all":
				context.UpgradeBridgeVersion = true
				context.UpgradeProviderVersion = true
			case "bridge":
				context.UpgradeBridgeVersion = true
			case "provider":
				context.UpgradeProviderVersion = true
			default:
				return fmt.Errorf(
					"--kind=%s invalid. Must be one of `all`, `bridge` or `provider`.",
					upgradeKind)
			}

			if context.MaxVersion != nil && !context.UpgradeProviderVersion {
				return fmt.Errorf(
					"cannot specify the provider version unless the provider will be upgraded")
			}

			return nil
		},
		Run: func(_ *cobra.Command, args []string) {
			err := upgrade.UpgradeProvider(context, args[0])
			exitOnError(err)
		},
	}

	cmd.PersistentFlags().StringVar(&maxVersion, "provider-version", "",
		`Upgrade the provider to the passed in version.

If the passed version does not exist, an error is signaled.`)

	cmd.PersistentFlags().BoolVar(&context.MajorVersionBump, "major", false,
		`Upgrade the provider to a new major version.`)

	cmd.PersistentFlags().StringVar(&upgradeKind, "kind", "all",
		`The kind of upgrade to perform:
- "all":     Upgrade the upstream provider and the bridge.
- "bridge":  Upgrade the bridge only.
- "provider: Upgrade the upstream provider only.`)

	return cmd
}

func main() {
	err := cmd().Execute()
	contract.IgnoreError(err)
}
