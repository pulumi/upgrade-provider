package step

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
			p := &spinnerDisplay{callstack: tt.stack}
			assert.Equal(t, strings.TrimPrefix(tt.expected, "\n"), p.callTree())
		})
	}
}
