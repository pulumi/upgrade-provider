package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"os"
	"strconv"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	"github.com/pulumi/upgrade-provider/colorize"
	"github.com/pulumi/upgrade-provider/upgrade"
)

const (
	// The name of our config file, without the file extension because viper supports many different config file languages.
	configFilename = ".upgrade-config"
	// The environment variable prefix of all environment variables bound to our command line flags.
	// For example, --number is bound to UPGRADE_NUMBER.
	envPrefix = "UPGRADE"
)

func cmd() *cobra.Command {
	var targetVersion string

	gopath, ok := os.LookupEnv("GOPATH")
	if !ok {
		gopath = build.Default.GOPATH
	}
	var upgradeKind []string
	var repoName string
	var repoOrg string
	var repoPath string

	ctx := context.Background()
	context := upgrade.Context{GoPath: gopath}

	exitOnError := func(err error) {
		if err == nil {
			return
		}
		if !errors.Is(err, upgrade.ErrHandled) {
			fmt.Printf("error: %s\n", err.Error())
		}
		os.Exit(1)
	}

	// If PersistentPreRunE returns an error, then cobra displays the error *and*
	// displays the result of `upgrade-provider --help`. If we don't want help to be
	// displayed, we can set failedPreRun and return. Run will immediately fail with
	// this error.
	var failedPreRun error
	cmd := &cobra.Command{
		Use:   "upgrade-provider <provider>",
		Short: "upgrade-provider automates the process of upgrading a TF-bridged provider",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			err := initializeConfig(cmd)
			if err != nil {
				failedPreRun = err
				return nil
			}
			// Validate argument is {org}/{repo}
			tok := strings.Split(args[0], "/")
			if len(tok) != 2 {
				return errors.New("argument must be provided as {org}/{repo}")
			}
			repoOrg, repoName = tok[0], tok[1]
			// repo name should start with 'pulumi-'
			if !strings.HasPrefix(repoName, "pulumi-") {
				return errors.New("{repo} must start with `pulumi-`")
			}
			// Require `upstream-provider-name` to be set
			if context.UpstreamProviderName == "" {
				return errors.New("`upstream-provider-name` must be provided")
			} else if strings.ContainsRune(context.UpstreamProviderName, '/') {
				var s string
				if split := strings.Split(context.UpstreamProviderName, "/"); len(split) > 1 {
					s = fmt.Sprintf(": try %q", split[len(split)-1])
				}
				return fmt.Errorf(`"upstream-provider-name" must not be fully qualified%s`, s)
			}

			// Validate that targetVersion is a valid version
			if targetVersion != "" {
				context.TargetVersion, err = semver.NewVersion(targetVersion)
				if err != nil {
					return fmt.Errorf("--target-version=%s: %w",
						targetVersion, err)
				}
			}

			// This can happen by calling `upgrade-provider --kind=""`
			if len(upgradeKind) == 0 {
				return fmt.Errorf("--kind=\"\" is invalid. Must be one of `all`, " +
					"`bridge`, `provider`, `java`, or `pulumi`")
			}

			// Validate the kind switch
			var warnedAll bool
			for _, kind := range upgradeKind {
				warn := func(msg string, a ...any) {
					fmt.Println(colorize.Warn(fmt.Sprintf(msg, a...)))
				}
				set := func(v *bool) {
					if *v && !warnedAll {
						warn("Duplicate `--kind` argument: %s", kind)
					}
					*v = true
				}

				switch kind {
				case "all":
					context.UpgradeBridgeVersion = true
					context.UpgradeProviderVersion = true
				case "bridge":
					set(&context.UpgradeBridgeVersion)
				case "provider":
					set(&context.UpgradeProviderVersion)
				case "java":
					context.UpgradeJavaVersion = true
					context.UpgradeJavaVersionOnly = true
				case "pulumi":
				case "check-upstream-version":
					if targetVersion != "" {
						return fmt.Errorf(
							"--kind check-upstream-version is incompatible with --target-version, cannot set both",
						)
					}
					set(&context.UpgradeProviderVersion)
					set(&context.OnlyCheckUpstream)

				default:
					return fmt.Errorf(
						"--kind=%s invalid. Must be one of `all`, `bridge`, `provider`, or `pulumi`",
						upgradeKind)
				}
			}
			// Set repoPath if specified
			context.SetRepoPath(repoPath)

			if context.TargetVersion != nil && !context.UpgradeProviderVersion {
				return fmt.Errorf(
					"cannot specify the provider version unless the provider will be upgraded")
			}
			return nil
		},
		Run: func(_ *cobra.Command, args []string) {
			exitOnError(failedPreRun)
			err := upgrade.UpgradeProvider(context.Wrap(ctx), repoOrg, repoName)
			exitOnError(err)
		},
	}

	pulumiDev, _ := strconv.ParseBool(os.Getenv("PULUMI_DEV"))

	cmd.PersistentFlags().BoolVar(&context.UpgradeJavaVersion, "upgrade-java", false,
		`Upgrade to the latest Java version`)
	err := cmd.PersistentFlags().MarkHidden("upgrade-java")
	contract.AssertNoErrorf(err, "could not mark `upgrade-java` flag as hidden")

	cmd.PersistentFlags().StringVar(&repoPath, "repo-path", "",
		`Clone the provider repo to the specified path. Skip cloning if set to "."`)

	cmd.PersistentFlags().StringVar(&targetVersion, "target-version", "",
		`Upgrade the provider to the passed version.

If the passed version does not exist, an error is signaled.`)

	cmd.PersistentFlags().VarP(upgrade.RefFlag(&context.TargetPulumiVersion), "target-pulumi-version", "",
		`Upgrade the provider to the passed pulumi/{pkg,sdk} version.

If no version is passed, the pulumi/{pkg,sdk} version will track the bridge`)

	cmd.PersistentFlags().BoolVar(&context.InferVersion, "pulumi-infer-version", false,
		`Use our GH issues to infer the target upgrade version.
If both '--target-version' and '--pulumi-infer-version' are passed,
we take '--target-version' to cap the inferred version. [Hidden behind PULUMI_DEV]`)

	if !pulumiDev {
		err = cmd.PersistentFlags().MarkHidden("pulumi-infer-version")
		contract.AssertNoErrorf(err, "could not mark `pulumi-infer-version` flag as hidden")
	}

	cmd.PersistentFlags().BoolVar(&context.MajorVersionBump, "major", false,
		`Upgrade the provider to a new major version.`)

	kindMsg := `The kind of upgrade to perform:

- "all": Upgrade the upstream provider and the bridge. Shorthand for "bridge,provider".
- "bridge": Upgrade the bridge only.
- "provider": Upgrade the upstream provider only.`
	if pulumiDev {
		kindMsg += `
- "check-upstream-version": Determine if we need to upgrade the upstream provider. For use in CI only."`
	}
	cmd.PersistentFlags().StringSliceVar(&upgradeKind, "kind",
		[]string{"all"}, kindMsg)

	cmd.PersistentFlags().StringVar(&context.UpstreamProviderName, "upstream-provider-name", "",
		`The name of the upstream provider.
Required unless running from provider root and set in upgrade-config.yml.`)

	cmd.PersistentFlags().StringVar(&context.UpstreamProviderOrg, "upstream-provider-org", "",
		`The name of the upstream provider's GitHub organization'.`)

	cmd.PersistentFlags().StringVar(&context.PrReviewers, "pr-reviewers", "",
		`A comma separated list of reviewers to assign the upgrade PR to.`)

	cmd.PersistentFlags().StringVar(&context.PrAssign, "pr-assign", "",
		`A user to assign the upgrade PR to.`)

	cmd.PersistentFlags().BoolVarP(&context.AllowMissingDocs, "allow-missing-docs", "", false,
		`If true, don't error on missing docs during tfgen.
This is equivalent to setting PULUMI_MISSING_DOCS_ERROR=${! VALUE}.`)

	cmd.PersistentFlags().StringVar(&context.JavaVersion, "java-version", "",
		"The version of pulumi-java-gen to target.")

	context.TargetBridgeRef = &upgrade.Latest{} // Set default
	cmd.PersistentFlags().VarP(upgrade.RefFlag(&context.TargetBridgeRef), "target-bridge-version", "",
		`The desired bridge version to upgrade to. Git hash references permitted.`)

	cmd.PersistentFlags().StringVar(&context.PRDescription, "pr-description", "",
		`Extra text to insert in the generated pull request description.`)

	cmd.PersistentFlags().StringVar(&context.PRTitlePrefix, "pr-title-prefix", "",
		`The prefix to insert in the generated pull request title.`)

	return cmd
}

func main() {
	err := cmd().Execute()
	if err != nil {
		os.Exit(1)
	}
}

// Adapted from https://github.com/carolynvs/stingoftheviper/blob/main/main.go
func initializeConfig(cmd *cobra.Command) error {
	v := viper.New()

	// Set the base name of the config file, without the file extension.
	v.SetConfigName(configFilename)

	// Set as many paths as you like where viper should look for the
	// config file. We are only looking in the current working directory.
	v.AddConfigPath(".")

	// Attempt to read the config file, gracefully ignoring errors
	// caused by a config file not being found. Return an error
	// if we cannot parse the config file.
	if err := v.ReadInConfig(); err != nil {
		// It's okay if there isn't a config file
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return err
		}
	}

	// When we bind flags to environment variables expect that the
	// environment variables are prefixed, e.g. a flag like --number
	// binds to an environment variable UPGRADE_NUMBER. This helps
	// avoid conflicts.
	v.SetEnvPrefix(envPrefix)

	// Environment variables can't have dashes in them, so bind them to their equivalent
	// keys with underscores, e.g. --upstream-provider-name to UPGRADE_UPSTREAM_PROVIDER_NAME
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	// Bind to environment variables
	// Works great for simple config names, but needs help for names
	// like --favorite-color which we fix in the bindFlags function
	v.AutomaticEnv()

	// Bind the current command's flags to viper
	bindFlags(cmd, v)

	return nil
}

// Bind each cobra flag to its associated viper configuration (config file and environment variable)
func bindFlags(cmd *cobra.Command, v *viper.Viper) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// Apply the viper config value to the flag when the flag is not set and viper has a value
		if !f.Changed && v.IsSet(f.Name) {
			val := v.Get(f.Name)
			err := cmd.Flags().Set(f.Name, fmt.Sprintf("%v", val))
			contract.AssertNoErrorf(err, "error setting flag")
		}
	})
}
