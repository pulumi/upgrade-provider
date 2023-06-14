package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

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
	var experimental bool
	var repoName string
	var repoOrg string
	var repoPath string

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
		Use:   "upgrade-provider <provider>",
		Short: "upgrade-provider automates the process of upgrading a TF-bridged provider",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			err := initializeConfig(cmd)
			if err != nil {
				return err
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
			}

			// Validate that targetVersion is a valid version
			if targetVersion != "" {
				context.TargetVersion, err = semver.NewVersion(targetVersion)
				if err != nil {
					return fmt.Errorf("--target-version=%s: %w",
						targetVersion, err)
				}
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
					context.UpgradeSdkVersion = true
					if experimental {
						context.UpgradeCodeMigration = true
					}
					if len(upgradeKind) > 1 {
						warnedAll = true
						warn("`--kind=all` implies all other options")
					}
				case "bridge":
					set(&context.UpgradeBridgeVersion)
				case "provider":
					set(&context.UpgradeProviderVersion)
				case "code":
					set(&context.UpgradeCodeMigration)
				case "sdk":
					set(&context.UpgradeSdkVersion)
				default:
					return fmt.Errorf(
						"--kind=%s invalid. Must be one of `all`, `bridge`, `provider`, or `code`.",
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
			err := upgrade.UpgradeProvider(context, repoOrg, repoName)
			if err != nil {
				msg, err := createFailureIssue(context, repoOrg, repoName, err.Error())
				if err != nil {
					fmt.Println(msg)
				}
			}
			exitOnError(err)
		},
	}

	cmd.PersistentFlags().StringVar(&repoPath, "repo-path", "",
		`Clone the provider repo to the specified path.`)

	cmd.PersistentFlags().StringVar(&targetVersion, "target-version", "",
		`Upgrade the provider to the passed version.

If the passed version does not exist, an error is signaled.`)

	cmd.PersistentFlags().BoolVar(&context.InferVersion, "pulumi-infer-version", false,
		`Use our GH issues to infer the target upgrade version.
		If both '--target-version' and '--pulumi-infer-version' are passed,
		we take '--target-version' to cap the inferred version. [Hidden behind PULUMI_DEV]`)
	err := cmd.PersistentFlags().MarkHidden("pulumi-infer-version")
	contract.AssertNoErrorf(err, "could not mark `pulumi-infer-version` flag as hidden")

	cmd.PersistentFlags().BoolVar(&context.MajorVersionBump, "major", false,
		`Upgrade the provider to a new major version.`)

	cmd.PersistentFlags().StringSliceVar(&upgradeKind, "kind", []string{"all"},
		`The kind of upgrade to perform:
- "all":     Upgrade the upstream provider and the bridge. Shorthand for "bridge,provider,code".
- "bridge":  Upgrade the bridge only.
- "provider": Upgrade the upstream provider only.
- "sdk": Upgrade the Pulumi sdk only.
- "code":     Perform some number of code migrations.`)

	cmd.PersistentFlags().BoolVar(&experimental, "experimental", false,
		`Enable experimental features, such as auto token mapping and auto aliasing`)

	cmd.PersistentFlags().StringSliceVar(&context.MigrationOpts, "migration-opts", nil,
		`A comma separated list of code migration to perform:
- "autoalias": Apply auto aliasing to the provider.`)

	cmd.PersistentFlags().StringVar(&context.UpstreamProviderName, "upstream-provider-name", "",
		`The name of the upstream provider.
Required unless running from provider root and set in upgrade-config.yml.`)

	cmd.PersistentFlags().BoolVar(&context.RemovePlugins, "remove-plugins", false,
		`Remove all pulumi plugins from cache before running the upgrade.
		It is possible that the generated examples may be non-deterministic depending on which
		plugins are used if existing versions are present in the cache.`)

	cmd.PersistentFlags().StringVar(&context.PrReviewers, "pr-reviewers", "",
		`A comma separated list of reviewers to assign the upgrade PR to.`)

	return cmd
}

func main() {
	err := cmd().Execute()
	contract.IgnoreError(err)
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

// Create an issue in the provider repo with a message describing the upgrade failure
func createFailureIssue(ctx upgrade.Context, repoOrg string, repoName string, errMsg string) (string, error) {
	now := time.Now()
	y, m, d := now.Date()
	title := fmt.Sprintf("Upgrade provider failure: %v %v %v", y, m, d)

	getIssues := exec.CommandContext(ctx, "gh", "search", "issues",
		"Upgrade provider failure: ",
		"--repo="+repoOrg+"/"+repoName,
		"--json=title,number",
	)
	bytes := new(bytes.Buffer)
	getIssues.Stdout = bytes
	err := getIssues.Run()
	if err != nil {
		return "Failed to create failure issue: failed to search existing issues", err
	}
	titles := []struct {
		Title  string `json:"title"`
		Number int    `json:"number"`
	}{}
	err = json.Unmarshal(bytes.Bytes(), &titles)
	if err != nil {
		return "Failed to create failure issue: failed to unmarshal gh output", err
	}
	for _, t := range titles {
		cmd := exec.Command("gh", "issue", "delete",
			fmt.Sprintf("%v", t.Number),
			"--repo="+repoOrg+"/"+repoName,
			"--yes",
		)
		if err := cmd.Run(); err != nil {
			return "Failed to create failure issue: failed to delete old failure issues", err
		}
	}

	cmd := exec.Command(
		"gh", "issue", "create",
		"--repo="+repoOrg+"/"+repoName,
		"--body="+fmt.Sprintf("%q", errMsg),
		"--title="+fmt.Sprintf("%q", title),
	)
	if err := cmd.Run(); err != nil {
		return "Failed to create failure issue: failed to create new issue", err
	}

	return "", err
}
