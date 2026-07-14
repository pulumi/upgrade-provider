package step

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandEnvironment(t *testing.T) {
	t.Setenv("UPGRADE_PROVIDER_STEP_TEST", "parent")

	var output string
	ctx := WithCommandEnv(context.Background(), map[string]string{
		"UPGRADE_PROVIDER_STEP_TEST": "child",
	})
	ok := Run(ctx, Cmd("sh", "-c", "printf %s \"$UPGRADE_PROVIDER_STEP_TEST\"").AssignTo(&output))

	require.True(t, ok)
	assert.Equal(t, "child", output)
	assert.Equal(t, "parent", os.Getenv("UPGRADE_PROVIDER_STEP_TEST"))
}

func TestCommandEnvironmentNestedOverride(t *testing.T) {
	ctx := WithCommandEnv(context.Background(), map[string]string{
		"UPGRADE_PROVIDER_STEP_FIRST":  "first",
		"UPGRADE_PROVIDER_STEP_SHARED": "outer",
	})
	ctx = WithCommandEnv(ctx, map[string]string{
		"UPGRADE_PROVIDER_STEP_SECOND": "second",
		"UPGRADE_PROVIDER_STEP_SHARED": "inner",
	})

	var output string
	ok := Run(ctx, Cmd("sh", "-c", `printf "%s|%s|%s" "$UPGRADE_PROVIDER_STEP_FIRST" "$UPGRADE_PROVIDER_STEP_SECOND" "$UPGRADE_PROVIDER_STEP_SHARED"`).AssignTo(&output))

	require.True(t, ok)
	assert.Equal(t, "first|second|inner", output)
}
