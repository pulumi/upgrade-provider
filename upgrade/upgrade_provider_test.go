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
//
// `mise ls ... go` prints the contents of $FAKE_MISE_LS_GO, if set.
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
if [ "$1" = "ls" ]; then
  for a in "$@"; do tool="$a"; done
  if [ "$tool" = "go" ] && [ -n "$FAKE_MISE_LS_GO" ]; then
    printf '%s' "$FAKE_MISE_LS_GO"
  fi
fi
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

func TestRunMiseUpgradeUninstallsStaleGoVersionsByExactVersion(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "mise-invocations.json")
	writeFakeMise(t, logFile)

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "provider"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "provider", "go.mod"),
		[]byte(providerGoModWithGoAndPulumi), // go 1.24.6
		0o600,
	))

	// The pre-bump Go selector is a prefix that matches several installed
	// versions, including the new one. `mise uninstall go@1.24` would fail
	// with "multiple tools specified" (and could remove 1.24.6).
	t.Setenv("GO_VERSION_MISE", "1.24")
	t.Setenv("PULUMI_VERSION_MISE", "")
	t.Setenv("FAKE_MISE_LS_GO", `[
  {"version": "1.24.2", "installed": true, "active": false},
  {"version": "1.24.6", "installed": true, "active": true},
  {"version": "1.24.10", "installed": true, "active": false},
  {"version": "1.25.0", "installed": true, "active": false}
]`)

	repo := ProviderRepo{root: root}
	err := stepv2.PipelineCtx(context.Background(), "test", func(ctx context.Context) {
		env := []stepv2.Env{&stepv2.SetCwd{To: root}}
		ctx = stepv2.WithEnv(ctx, env...)
		current := map[string]*stepv2.EnvVar{}
		_, _ = runMiseUpgrade(ctx, repo, &env, current)
	})
	require.NoError(t, err)

	var uninstalls [][]string
	for _, argv := range readMiseInvocations(t, logFile) {
		if len(argv) > 0 && argv[0] == "uninstall" {
			uninstalls = append(uninstalls, argv)
		}
	}
	assert.Equal(t, [][]string{
		{"uninstall", "go@1.24.2"},
		{"uninstall", "go@1.24.10"},
	}, uninstalls)
}

func TestStaleMiseVersions(t *testing.T) {
	installed := []string{"1.25.9", "1.26.0", "1.26.1", "1.26.2", "1.260.0", "1.27.0"}

	tests := []struct {
		name     string
		old, new string
		expected []string
	}{
		{"prefix to exact within same minor", "1.26", "1.26.2", []string{"1.26.0", "1.26.1"}},
		{"prefix to prefix", "1.26", "1.27", []string{"1.26.0", "1.26.1", "1.26.2"}},
		{"exact to exact", "1.26.1", "1.26.2", []string{"1.26.1"}},
		{"exact old matched by new prefix", "1.26.1", "1.26", nil},
		{"old not installed", "1.24", "1.26", nil},
		{"v prefix", "v1.26.0", "v1.26.1", []string{"1.26.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, staleMiseVersions(installed, tt.old, tt.new))
		})
	}
}

func TestParseMiseInstalledVersions(t *testing.T) {
	versions, err := parseMiseInstalledVersions("")
	require.NoError(t, err)
	assert.Empty(t, versions)

	versions, err = parseMiseInstalledVersions(`[
  {"version": "1.25.1", "install_path": "/x/1.25.1", "installed": true, "active": false},
  {"version": "1.25.2", "install_path": "/x/1.25.2", "installed": false, "active": false},
  {"version": "1.25.3", "install_path": "/x/1.25.3"}
]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"1.25.1", "1.25.3"}, versions)

	_, err = parseMiseInstalledVersions("not json")
	assert.Error(t, err)
}
