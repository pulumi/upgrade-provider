package step_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/upgrade-provider/step/v2"
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

			s := step.ReadFile(ctx, "text.txt")
			assert.Equal(t, "abc", s)

			pwd := strings.TrimSpace(step.Cmd(ctx, "pwd"))
			assert.True(t, strings.HasSuffix(pwd, dir))
		})
	})

	assert.NoError(t, err)
}

func TestEnvScoping(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	dir3 := "dir3" // This is relative to dir2
	target := filepath.Join(dir2, dir3)
	err := os.Mkdir(target, 0700)
	require.NoError(t, err)

	checkPwd := func(t *testing.T, ctx context.Context, target string) {
		pwd := step.Cmd(ctx, "pwd")
		t.Logf("PWD: %q", pwd)
		t.Logf("Target: %q", target)
		assert.True(t, strings.HasSuffix(pwd, target+"\n"))
	}

	t.Run("raw", func(t *testing.T) {
		err = step.Pipeline("test", func(ctx context.Context) {
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir1})
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir2}, &step.SetCwd{To: dir3})

			checkPwd(t, ctx, target)
		})
	})

	t.Run("scoped", func(t *testing.T) {
		err = step.Pipeline("test", func(ctx context.Context) {
			ctx = step.WithEnv(ctx, &step.SetCwd{To: dir1})
			step.WithCwd(ctx, dir2, func(ctx context.Context) {
				checkPwd(t, ctx, dir2)
			})
			checkPwd(t, ctx, dir1)
		})
	})
}
