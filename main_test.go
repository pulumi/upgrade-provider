package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelpShowsDefaultFlagValues(t *testing.T) {
	command := cmd()

	buf := new(bytes.Buffer)
	command.SetOut(buf)
	command.SetErr(buf)
	command.SetArgs([]string{"--help"})

	require.NoError(t, command.Execute())

	help := buf.String()

	// Boolean flags should document their default value, even though
	// `false` is the zero value, so users can tell what the default
	// behavior is without reading the source.
	for _, name := range []string{"allow-major", "allow-missing-docs", "dry-run", "major"} {
		flag := command.PersistentFlags().Lookup(name)
		require.NotNil(t, flag, "flag %q not found", name)
		require.Contains(t, flag.Usage, "(default: false)",
			"flag %q usage should document its default value", name)
	}

	// String/slice flags already have their non-zero default shown by
	// Cobra's standard formatting, unaffected by the change above.
	require.Contains(t, help, "--kind strings")
	require.Contains(t, help, "(default [all])")
	require.Contains(t, help, "--target-bridge-version ref")
	require.Contains(t, help, "(default <latest>)")

	// Flags with an empty/zero default should still show no default at all.
	require.Contains(t, help, "--pr-assign string ")
	require.False(t, strings.Contains(help, "--pr-assign string (default"))

	// Cobra's own `--help` flag should be untouched.
	require.Contains(t, help, "-h, --help ")
	require.False(t, strings.Contains(help, "--help (default"))
}

func TestInitializeConfigBindsAllowMajor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".upgrade-config.yml"), []byte("allow-major: true\n"), 0o600))

	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(previous))
	})

	command := cmd()
	require.NoError(t, initializeConfig(command))

	flag := command.PersistentFlags().Lookup("allow-major")
	require.NotNil(t, flag)
	require.Equal(t, "true", flag.Value.String())
}

func TestBuildVersion(t *testing.T) {
	originalVersion, originalCommit := version, commit
	t.Cleanup(func() {
		version, commit = originalVersion, originalCommit
	})

	t.Run("version and commit set via ldflags", func(t *testing.T) {
		version, commit = "v1.2.3", "3212adb3abcd"
		require.Equal(t, "v1.2.3-3212adb3", buildVersion())
	})

	t.Run("only version set", func(t *testing.T) {
		version, commit = "v1.2.3", ""
		require.Equal(t, "v1.2.3", buildVersion())
	})
}

func TestCmdReportsVersion(t *testing.T) {
	originalVersion, originalCommit := version, commit
	version, commit = "v1.2.3", "3212adb3"
	t.Cleanup(func() {
		version, commit = originalVersion, originalCommit
	})

	command := cmd()
	require.Equal(t, "v1.2.3-3212adb3", command.Version)
	require.Contains(t, command.Long, "Version: v1.2.3-3212adb3")
}
