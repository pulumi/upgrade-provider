package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetRepoExpectedLocation(t *testing.T) {
	ctx := Context{
		GoPath: "/Users/myuser/go",
	}

	mockRepoPath := filepath.Join("github.com", "pulumi", "random-provider")
	defaultExpectedLocation := filepath.Join(ctx.GoPath, "src", mockRepoPath)

	baseProviderCwd := string(os.PathSeparator) + filepath.Join("Users", "home", mockRepoPath)
	subProviderCwd := filepath.Join(baseProviderCwd, "examples")
	randomCwd := string(os.PathSeparator) + filepath.Join("Users", "random", "dir")

	// test cwd == repo path
	tests := []struct{ cwd, repoPath, expected string }{
		{baseProviderCwd, mockRepoPath, baseProviderCwd},   // expected set to cwd
		{subProviderCwd, mockRepoPath, baseProviderCwd},    // expected set to top level of cwd repo path
		{randomCwd, mockRepoPath, defaultExpectedLocation}, // expected set to default on no match
	}

	for _, tt := range tests {
		tt := tt
		t.Run(fmt.Sprintf("(%s,%s,%s)", tt.cwd, tt.repoPath, tt.expected), func(t *testing.T) {
			expected, err := getRepoExpectedLocation(ctx, tt.cwd, tt.repoPath)
			expected = trimSeparators(expected)
			assert.Nil(t, err)
			assert.Equal(t, trimSeparators(tt.expected), expected)
		})
	}
}

func trimSeparators(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, string(os.PathSeparator)),
		string(os.PathSeparator))
}
