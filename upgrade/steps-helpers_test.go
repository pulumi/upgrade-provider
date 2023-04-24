package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetRepoExpectedLocation(t *testing.T) {
	ctx := Context{
		GoPath: "Users/myuser/go",
	}

	cwd, err := os.Getwd()
	assert.Nil(t, err)

	// test cwd == repo path
	mockRepoPath := cwd
	expected, err := getRepoExpectedLocation(ctx, mockRepoPath)
	mockRepoPath = trimSeparators(mockRepoPath)
	expected = trimSeparators(expected)
	assert.Nil(t, err)
	assert.Equal(t, mockRepoPath, expected)

	// test directory above cwd == repo path
	mockRepoPath = strings.TrimSuffix(cwd, filepath.Base(cwd))
	expected, err = getRepoExpectedLocation(ctx, mockRepoPath)
	assert.Nil(t, err)
	mockRepoPath = trimSeparators(mockRepoPath)
	expected = trimSeparators(expected)
	assert.Equal(t, mockRepoPath, expected)

	// test cwd completely different from repo path
	mockRepoPath = filepath.Join("github.com", "pulumi", "random-provider")
	expected, err = getRepoExpectedLocation(ctx, mockRepoPath)
	expected = trimSeparators(expected)
	assert.Nil(t, err)
	assert.Equal(t, filepath.Join(ctx.GoPath, "src", mockRepoPath), expected)

}

func trimSeparators(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, string(os.PathSeparator)),
		string(os.PathSeparator))
}
