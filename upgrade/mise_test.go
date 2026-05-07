package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

func TestRunMiseUpgradeRefreshesFromUpdatedProviderGoMod(t *testing.T) {
	t.Setenv("PULUMI_VERSION_MISE", "3.228.0")
	t.Setenv("GO_VERSION_MISE", "1.25.8")

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	logPath := filepath.Join(tempDir, "mise.log")
	t.Setenv("MISE_TEST_LOG", logPath)

	misePath := filepath.Join(binDir, "mise")
	require.NoError(t, os.WriteFile(misePath, []byte(`#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
upgrade)
	echo "upgrade PULUMI_VERSION_MISE=${PULUMI_VERSION_MISE:-} GO_VERSION_MISE=${GO_VERSION_MISE:-}" >> "${MISE_TEST_LOG}"
	;;
env)
	echo "env PULUMI_VERSION_MISE=${PULUMI_VERSION_MISE:-} GO_VERSION_MISE=${GO_VERSION_MISE:-}" >> "${MISE_TEST_LOG}"
	printf '{"PULUMI_VERSION_MISE":"%s","GO_VERSION_MISE":"%s"}\n' "${PULUMI_VERSION_MISE:-}" "${GO_VERSION_MISE:-}"
	;;
*)
	echo "unexpected mise command: $*" >&2
	exit 2
	;;
esac
`), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repoRoot := filepath.Join(tempDir, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "provider"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "provider", "go.mod"), []byte(`module example.com/provider

go 1.25.9

require github.com/pulumi/pulumi/sdk/v3 v3.234.0
`), 0o600))

	err := stepv2.PipelineCtx(context.Background(), "test mise refresh", func(ctx context.Context) {
		repo := ProviderRepo{root: repoRoot}
		env := []stepv2.Env{&stepv2.SetCwd{To: repo.root}}
		ctx = stepv2.WithEnv(ctx, env...)
		miseEnvVars := map[string]*stepv2.EnvVar{}

		var ok bool
		ctx, ok = runMiseUpgrade(ctx, repo, &env, miseEnvVars)
		if !ok {
			stepv2.HaltOnError(ctx, errors.New("mise not available"))
		}
		ctx = refreshMiseEnv(ctx, &env, miseEnvVars)

		stepv2.Cmd(ctx, "bash", "-c", fmt.Sprintf(
			"echo downstream PULUMI_VERSION_MISE=${PULUMI_VERSION_MISE:-} GO_VERSION_MISE=${GO_VERSION_MISE:-} >> %q",
			logPath))
	}, stepv2.NullDisplay)
	require.NoError(t, err)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, strings.Join([]string{
		"upgrade PULUMI_VERSION_MISE=3.234.0 GO_VERSION_MISE=1.25.9",
		"env PULUMI_VERSION_MISE=3.234.0 GO_VERSION_MISE=1.25.9",
		"downstream PULUMI_VERSION_MISE=3.234.0 GO_VERSION_MISE=1.25.9",
		"",
	}, "\n"), string(log))
}
