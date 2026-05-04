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
