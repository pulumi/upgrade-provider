package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
