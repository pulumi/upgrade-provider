package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/hexops/autogold/v2"
	"github.com/stretchr/testify/assert"
	"golang.org/x/mod/module"
)

func TestGetRepoExpectedLocation(t *testing.T) {
	ctx := &Context{
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
			expected, err := getRepoExpectedLocation(ctx.Wrap(context.Background()), tt.cwd, tt.repoPath)
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

func TestPullRequestBody(t *testing.T) {
	t.Run("description-space", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("description-equal", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{PRDescription: "Some extra description here with links to pulumi/repo#123"}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description=" + uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, nil, nil, nil, "", args)
		autogold.ExpectFile(t, got)
	})

	t.Run("upgrades", func(t *testing.T) {
		ctx := context.Background()
		uc := Context{
			UpgradePfVersion:     true,
			UpgradeBridgeVersion: true,
		}
		args := []string{"upgrade-provider", "--kind", "bridge", "--pr-description", uc.PRDescription}
		got := prBody(uc.Wrap(ctx), ProviderRepo{}, nil, &GoMod{
			Bridge: module.Version{Version: "v1.2.2"},
			Pf:     module.Version{Version: "v4.5.5"},
		},
			&Version{SemVer: semver.MustParse("v1.2.3")},
			&Version{SemVer: semver.MustParse("v4.5.6")}, "", args)
		autogold.ExpectFile(t, got)
	})
}
