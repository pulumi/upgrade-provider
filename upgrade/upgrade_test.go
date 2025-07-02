package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/upgrade-provider/step/v2"
)

func TestBridgeUpgradeNoop(t *testing.T) {
	ctx := newReplay(t, "gcp_noop_bridge_update")
	ctx = (&Context{
		GoPath:               "/goPath",
		UpgradeBridgeVersion: true,
		TargetBridgeRef:      &Latest{},
		UpstreamProviderOrg:  "hashicorp",
	}).Wrap(ctx)

	err := step.CallWithReplay(ctx, "Plan Upgrade",
		"Planning Bridge Upgrade", planBridgeUpgrade)
	require.NoError(t, err)

	assert.False(t, GetContext(ctx).UpgradeBridgeVersion)
}

func newReplay(t *testing.T, name string) context.Context {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join("testdata", "replay", name+".json")
	bytes := readFile(t, path)
	r := step.NewReplay(t, bytes)
	return step.WithEnv(ctx, r)
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	return bytes
}

func testReplay(ctx context.Context, t *testing.T, stepReplay []*step.Step, fName string, f any) {
	t.Helper()
	bytes, err := json.Marshal(step.ReplayV1{
		Pipelines: []step.RecordV1{{
			Name:  t.Name(),
			Steps: stepReplay,
		}},
	})
	require.NoError(t, err)

	r := step.NewReplay(t, bytes)
	ctx = step.WithEnv(ctx, r)

	err = step.CallWithReplay(ctx, t.Name(), fName, f)
	assert.NoError(t, err)
}

func jsonMarshal[T any](t *testing.T, content string) T {
	t.Helper()
	var dst T
	err := json.Unmarshal([]byte(content), &dst)
	require.NoError(t, err)
	return dst
}
