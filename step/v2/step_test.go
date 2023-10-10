package step

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallTree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		stack    []string
		expected string
	}{
		{
			stack: []string{"foo", "bar", "fizz", "", "", "bar2"},
			expected: `
  - foo
    - bar
      - fizz
   -> bar2
`,
		},
		{
			stack:    []string{"foo"},
			expected: " -> foo\n",
		},
		{
			stack: []string{"foo", "", "bar"},
			expected: `
  - foo
 -> bar
`,
		},
		{
			stack: []string{"foo", "bar", "", "bar2", ""},
			expected: `
 -> foo
    - bar
    - bar2
`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("", func(t *testing.T) {
			t.Parallel()
			p := &pipeline{callstack: tt.stack}
			assert.Equal(t, strings.TrimPrefix(tt.expected, "\n"), p.callTree())
		})
	}
}

func TestPipeline(t *testing.T) {
	t.Parallel()
	var result int
	err := Pipeline("test", func(ctx context.Context) {
		one := Call01(ctx, "one", func(context.Context) int {
			return 1
		})
		result = Call11(ctx, "+2", func(_ context.Context, one int) int {
			return one + 2
		}, one)
	})
	require.NoError(t, err)
	assert.Equal(t, 3, result)
}

func TestCmd(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var txt string
		err := Pipeline("cmd", func(ctx context.Context) {
			txt = Cmd(ctx, "echo", "hello, world")
		})
		require.NoError(t, err)
		assert.Equal(t, "hello, world\n", txt)
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		err := Pipeline("cmd", func(ctx context.Context) {
			Cmd(ctx, "does-not-exist-so-this-should-fail")
		})
		require.Error(t, err)
	})
}

func TestNestedError(t *testing.T) {
	t.Parallel()
	expectedErr := fmt.Errorf("an error")
	err := Pipeline("test", func(ctx context.Context) {
		Call00(ctx, "n1", func(ctx context.Context) {
			Call00(ctx, "n2", func(ctx context.Context) {
				Call00E(ctx, "n3", func(ctx context.Context) error {
					return expectedErr
				})
			})
		})
	})
	require.Equal(t, expectedErr, err)
}

func TestEnv(t *testing.T) {
	var result string
	err := Pipeline("test", func(ctx context.Context) {
		result = Cmd(WithEnv(ctx, &EnvVar{
			Key:   "ENV",
			Value: "result",
		}), "bash", "-c", "echo $ENV")
	})
	require.NoError(t, err)
	assert.Equal(t, "result\n", result)
}
