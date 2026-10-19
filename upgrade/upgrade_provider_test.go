package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

// writeFakeMise installs a fake "mise" executable on PATH that records every
// invocation (as a JSON array of argv slices) to logFile. It exits
// successfully for every subcommand it needs to support in these tests.
//
// Current versions are read from $FAKE_MISE_CURRENT_DIR/<tool>-<selector>.json.
// An unfiltered Go listing returns $FAKE_MISE_LS_GO, the shared inventory.
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
  if [ "$2" = "--current" ]; then
    case "$tool" in
      go) selector="$GO_VERSION_MISE" ;;
      github:pulumi/pulumi) tool="pulumi"; selector="$PULUMI_VERSION_MISE" ;;
    esac
    fixture="$FAKE_MISE_CURRENT_DIR/$tool-$selector.json"
    if [ -f "$fixture" ]; then
      cat "$fixture"
    else
      printf '[]'
    fi
  elif [ "$tool" = "go" ] && [ -n "$FAKE_MISE_LS_GO" ]; then
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

func TestRunMiseUpgradeOnlyUninstallsPreviouslySelectedVersions(t *testing.T) {
	tests := []struct {
		name         string
		oldGo        string
		oldPulumi    string
		secondPass   bool
		missingNewGo bool
		expected     [][]string
	}{
		{
			name:  "preserve unrelated installed versions",
			oldGo: "1.24.10", oldPulumi: "3.186.2",
			expected: [][]string{{"uninstall", "github:pulumi/pulumi@3.186.2"}, {"uninstall", "go@1.24.10"}},
		},
		{
			name:  "selectors resolve to the same installations",
			oldGo: "1.24.6", oldPulumi: "3.187.0",
		},
		{
			name: "old versions are not installed",
		},
		{
			name:  "do not remove old Go when the new installation cannot be resolved",
			oldGo: "1.24.10", oldPulumi: "3.187.0", missingNewGo: true,
		},
		{
			name:  "second pass cleans up the first pass versions",
			oldGo: "1.24.10", oldPulumi: "3.186.2", secondPass: true,
			expected: [][]string{
				{"uninstall", "github:pulumi/pulumi@3.186.2"}, {"uninstall", "go@1.24.10"},
				{"uninstall", "github:pulumi/pulumi@3.187.0"}, {"uninstall", "go@1.24.6"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "mise-invocations.json")
			writeFakeMise(t, logFile)
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "provider"), 0o755))
			modFile := filepath.Join(root, "provider", "go.mod")
			require.NoError(t, os.WriteFile(modFile, []byte(providerGoModWithGoAndPulumi), 0o600))

			// Prefix selectors may resolve to only one of several shared installs.
			t.Setenv("GO_VERSION_MISE", "1.24")
			t.Setenv("PULUMI_VERSION_MISE", "3.186")
			t.Setenv("FAKE_MISE_LS_GO", `[
  {"version":"1.24.2","installed":true},
  {"version":"1.24.6","installed":true},
  {"version":"1.24.10","installed":true},
  {"version":"1.25.0","installed":true}
]`)
			fixtures := t.TempDir()
			t.Setenv("FAKE_MISE_CURRENT_DIR", fixtures)
			for name, version := range map[string]string{
				"go-1.24": tt.oldGo, "go-1.24.6": "1.24.6", "go-1.25.1": "1.25.1",
				"pulumi-3.186": tt.oldPulumi, "pulumi-3.187.0": "3.187.0", "pulumi-3.188.0": "3.188.0",
			} {
				if tt.missingNewGo && name == "go-1.24.6" {
					version = ""
				}
				entries := []map[string]any{}
				if version != "" {
					entries = append(entries, map[string]any{"version": version, "installed": true})
				}
				data, err := json.Marshal(entries)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(fixtures, name+".json"), data, 0o600))
			}

			repo := ProviderRepo{root: root}
			err := stepv2.PipelineCtx(context.Background(), "test", func(ctx context.Context) {
				env := []stepv2.Env{&stepv2.SetCwd{To: root}}
				ctx = stepv2.WithEnv(ctx, env...)
				current := map[string]*stepv2.EnvVar{}
				ctx, _ = runMiseUpgrade(ctx, repo, &env, current)
				if tt.secondPass {
					// Tidy can raise the versions required by provider/go.mod.
					updated := strings.ReplaceAll(providerGoModWithGoAndPulumi, "1.24.6", "1.25.1")
					updated = strings.ReplaceAll(updated, "3.187.0", "3.188.0")
					require.NoError(t, os.WriteFile(modFile, []byte(updated), 0o600))
					runMiseUpgrade(ctx, repo, &env, current)
				}
			})
			if tt.missingNewGo {
				require.ErrorContains(t, err, "mise did not resolve an installed current version of go")
			} else {
				require.NoError(t, err)
			}

			var uninstalls [][]string
			for _, argv := range readMiseInvocations(t, logFile) {
				if argv[0] == "ls" {
					assert.Equal(t, []string{"--current", "--installed", "--json"}, argv[1:len(argv)-1])
				}
				if argv[0] == "uninstall" {
					uninstalls = append(uninstalls, argv)
				}
			}
			assert.Equal(t, tt.expected, uninstalls)
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
