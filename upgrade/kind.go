package upgrade

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

type RepoKind string

const (
	Plain             RepoKind = "plain"
	Shimmed           RepoKind = "shimmed"
	Patched           RepoKind = "patched"
	PatchedAndShimmed RepoKind = "patched & shimmed"
)

func (rk RepoKind) Shimmed() RepoKind {
	switch rk {
	case Plain:
		return Shimmed
	default:
		return rk
	}
}

func (rk RepoKind) Patched() RepoKind {
	switch rk {
	case Plain:
		return Patched
	case Shimmed:
		return PatchedAndShimmed
	default:
		return rk
	}
}

func (rk RepoKind) IsShimmed() bool {
	switch rk {
	case Shimmed:
		return true
	default:
		return false
	}
}

func (rk RepoKind) IsPatched() bool {
	switch rk {
	case Patched:
		fallthrough
	case PatchedAndShimmed:
		return true
	default:
		return false
	}
}

var getRepoKind = stepv2.Func11E("Get Repo Kind", func(ctx context.Context, repo ProviderRepo) (*GoMod, error) {
	path := repo.root
	file := filepath.Join(path, "provider", "go.mod")

	data := stepv2.ReadFile(ctx, file)

	goMod, err := modfile.Parse(file, []byte(data), nil)
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}

	bridge, ok, err := originalGoVersionOf(ctx, repo, filepath.Join("provider", "go.mod"), "github.com/pulumi/pulumi-terraform-bridge")
	bridgeMissingMsg := "Unable to discover pulumi-terraform-bridge version"
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bridgeMissingMsg, err)
	} else if !ok {
		return nil, fmt.Errorf("%s", bridgeMissingMsg)
	}

	tfProviderRepoName := GetContext(ctx).UpstreamProviderName

	getUpstream := func(file *modfile.File) (*modfile.Require, error) {
		// Find the name of our upstream dependency
		for _, mod := range file.Require {
			pathWithoutVersion := modPathWithoutVersion(mod.Mod.Path)
			if strings.HasSuffix(pathWithoutVersion, tfProviderRepoName) {
				return mod, nil
			}
		}
		return nil, fmt.Errorf("could not find upstream '%s' in go.mod", tfProviderRepoName)
	}

	var upstream *modfile.Require
	var patched bool
	patchDir := filepath.Join(path, "upstream")
	if _, hasPatch := stepv2.Stat(ctx, patchDir); hasPatch {
		patched = true
	}

	shimDir := filepath.Join(path, "provider", "shim")
	var shimmed bool
	if _, hasShim := stepv2.Stat(ctx, shimDir); hasShim {
		shimmed = true
		modPath := filepath.Join(shimDir, "go.mod")
		data := stepv2.ReadFile(ctx, modPath)
		shimMod, err := modfile.Parse(modPath, []byte(data), nil)
		if err != nil {
			return nil, fmt.Errorf("shim/go.mod: %w", err)
		}
		upstream, err = getUpstream(shimMod)
		if err != nil {
			return nil, fmt.Errorf("shim/go.mod: %w", err)
		}
	} else {
		upstream, err = getUpstream(goMod)
		if err != nil {
			return nil, fmt.Errorf("go.mod: %w", err)
		}
	}

	contract.Assertf(upstream != nil, "upstream cannot be nil")

	out := GoMod{
		Upstream: upstream.Mod,
		Bridge:   bridge,
	}

	out.Kind = Plain

	if shimmed {
		out.Kind = out.Kind.Shimmed()
	}
	if patched {
		out.Kind = out.Kind.Patched()
	}

	return &out, nil
})
