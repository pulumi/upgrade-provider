package step_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/upgrade-provider/step/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithCwd(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte("abc"), 0600)
	require.NoError(t, err)
	err = step.Pipeline("test", func(ctx context.Context) {
		step.WithCwd(ctx, dir, func(ctx context.Context) {
			f, err := os.ReadFile("text.txt")
			require.NoError(t, err)
			assert.Equal(t, f, []byte("abc"))
		})
	})

	assert.NoError(t, err)
}
