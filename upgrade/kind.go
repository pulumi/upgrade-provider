package upgrade

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"

	stepv2 "github.com/pulumi/upgrade-provider/step/v2"
)

type RepoKind string

const (
	Plain             RepoKind = "plain"
	Forked            RepoKind = "forked"
	Shimmed           RepoKind = "shimmed"
	ForkedAndShimmed  RepoKind = "forked & shimmed"
	Patched           RepoKind = "patched"
	PatchedAndShimmed RepoKind = "patched & shimmed"
)

func (rk RepoKind) Shimmed() RepoKind {
	switch rk {
	case Plain:
		return Shimmed
	case Forked:
		return ForkedAndShimmed
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
	case Forked, ForkedAndShimmed:
		panic("Cannot have a forked and patched provider")
	default:
		return rk
	}
}

func (rk RepoKind) IsForked() bool {
	switch rk {
	case Forked:
		fallthrough
	case ForkedAndShimmed:
		return true
	default:
		return false
	}
}

func (rk RepoKind) IsShimmed() bool {
	switch rk {
	case Shimmed:
		fallthrough
	case ForkedAndShimmed:
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

	pf, ok, err := originalGoVersionOf(ctx, repo, filepath.Join("provider", "go.mod"), "github.com/pulumi/pulumi-terraform-bridge/pf")
	if err != nil {
		return nil, err
	} else if !ok {
		// If we successfully opened provider/go.mod but didn't find any reference
		// to "github.com/pulumi/pulumi-terraform-bridge/pf", we assume that the
		// provider doesn't use /pf and it doesn't make sense to upgrade.
		GetContext(ctx).UpgradePfVersion = false
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

	// If we find a replace that points to a pulumi hosted repo, that indicates a fork.
	var fork *modfile.Replace
	for _, replace := range goMod.Replace {
		// If we're not replacing our upstream, we don't care here
		if replace.Old.Path != upstream.Mod.Path {
			continue
		}
		hasMajorVersion := func(path string) bool {
			_, major, ok := module.SplitPathVersion(path)
			return ok && major != ""
		}
		before, after, found := strings.Cut(replace.New.Path, "/"+tfProviderRepoName)
		if !found || (after != "" && !hasMajorVersion(after)) {
			if replace.New.Path == "../upstream" {
				// We have found a patched provider, so we can just exit here.
				break
			}
			return nil, fmt.Errorf("go.mod: replace has incorrect repo: '%s'", replace.New.Path)
		}
		repoOrgSeperator := strings.LastIndexByte(before, '/')
		org := before[repoOrgSeperator+1:]
		if org != "pulumi" {
			// We have a replace directive for upstream, but it doesn't point
			// to a pulumi fork. For the purposes of this tool, this is not a
			// *forked* provider.
			break
		}
		fork = replace
		break
	}

	out := GoMod{
		Upstream: upstream.Mod,
		Fork:     fork,
		Bridge:   bridge,
		Pf:       pf,
	}

	if fork == nil {
		out.Kind = Plain
	} else {
		out.Kind = Forked
	}

	if shimmed {
		out.Kind = out.Kind.Shimmed()
	}
	if patched {
		out.Kind = out.Kind.Patched()
	}

	return &out, nil
})
