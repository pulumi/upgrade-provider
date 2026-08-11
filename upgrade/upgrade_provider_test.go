package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

// writeFakeMise installs a fake "mise" executable on PATH that records every
// invocation (as a JSON array of argv slices) to logFile. It exits
// successfully for every subcommand it needs to support in these tests.
func writeFakeMise(t *testing.T, logFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script requires a POSIX shell")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
args=""
sep=""
for a in "$@"; do
  args="$args$sep\"$a\""
  sep=","
done
printf '[%s]\n' "$args" >> "` + logFile + `"
exit 0
`
	misePath := filepath.Join(binDir, "mise")
	require.NoError(t, os.WriteFile(misePath, []byte(script), 0o755))

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// readMiseInvocations parses the invocation log written by the fake mise
// binary into a slice of argv slices, one per invocation.
func readMiseInvocations(t *testing.T, logFile string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)

	var invocations [][]string
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var argv []string
		require.NoError(t, dec.Decode(&argv))
		invocations = append(invocations, argv)
	}
	return invocations
}

const providerGoModWithGoAndPulumi = `module github.com/pulumi/pulumi-fake/provider

go 1.24.6

require github.com/pulumi/pulumi/sdk/v3 v3.187.0
`

func TestRunMiseUpgradeScopesInstallAndUpgradeToGoAndPulumi(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "mise-invocations.json")
	writeFakeMise(t, logFile)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "provider"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "provider", "go.mod"),
		[]byte(providerGoModWithGoAndPulumi),
		0o600,
	))

	// The repository's Mise config also manages an unrelated tool with a
	// floating version constraint (e.g. Node.js, as in
	// https://github.com/pulumi/upgrade-provider/issues/386). It must never
	// be passed to `mise install` or `mise upgrade`.
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "mise.toml"),
		[]byte("[tools]\nnodejs = \"22\"\n"),
		0o600,
	))

	repo := ProviderRepo{root: root}

	var available bool
	err := stepv2.PipelineCtx(context.Background(), "test", func(ctx context.Context) {
		env := []stepv2.Env{&stepv2.SetCwd{To: root}}
		ctx = stepv2.WithEnv(ctx, env...)
		current := map[string]*stepv2.EnvVar{}
		_, available = runMiseUpgrade(ctx, repo, &env, current)
	})
	require.NoError(t, err)
	require.True(t, available, "expected the fake mise binary to be found on PATH")

	invocations := readMiseInvocations(t, logFile)

	var installArgs, upgradeArgs []string
	for _, argv := range invocations {
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "install":
			installArgs = argv
		case "upgrade":
			upgradeArgs = argv
		}
	}
	require.NotEmpty(t, installArgs, "expected a `mise install` invocation, got: %v", invocations)
	require.NotEmpty(t, upgradeArgs, "expected a `mise upgrade` invocation, got: %v", invocations)

	assert.Equal(t, []string{"install", "go", "github:pulumi/pulumi"}, installArgs)
	assert.Equal(t, []string{"upgrade", "--raw", "go", "github:pulumi/pulumi"}, upgradeArgs)

	for _, argv := range [][]string{installArgs, upgradeArgs} {
		assert.NotContains(t, argv, "nodejs")
		assert.NotContains(t, argv, "node")
	}
}
