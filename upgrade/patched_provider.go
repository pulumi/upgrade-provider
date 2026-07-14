package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pulumi/upgrade-provider/step"
)

const patchCheckoutBranch = "pulumi/patch-checkout"

func patchedProviderUpgradeStep(repo ProviderRepo, targetRef string) step.Step {
	upstreamDir := filepath.Join(repo.root, "upstream")
	steps := []step.Step{
		step.F("Check patched provider state", func(ctx context.Context) (string, error) {
			return checkPatchedProviderPreflight(ctx, upstreamDir, targetRef)
		}),
	}
	for _, command := range patchedProviderUpgradeCommands(targetRef) {
		steps = append(steps, step.Cmd(command[0], command[1:]...).In(&repo.root))
	}
	return step.Combined("update patched provider", steps...)
}

func patchedProviderUpgradeCommands(targetRef string) [][]string {
	return [][]string{
		{"./scripts/upstream.sh", "checkout"},
		{"./scripts/upstream.sh", "rebase", "-o", targetRef},
		{"./scripts/upstream.sh", "check_in"},
	}
}

// checkPatchedProviderPreflight refuses to interpret or modify interrupted
// patch workflows. Recovery is deliberately manual because Git cannot prove
// that every patch was applied before an interruption.
func checkPatchedProviderPreflight(
	ctx context.Context, upstreamDir, targetRef string,
) (string, error) {
	initialized, err := patchedProviderInitialized(upstreamDir)
	if err != nil {
		return "", err
	}
	if !initialized {
		return "upstream submodule not yet initialized", nil
	}

	operationActive, err := hasActivePatchedGitOperation(ctx, upstreamDir)
	if err != nil {
		return "", err
	}
	if operationActive {
		return "", fmt.Errorf(`the patched upstream repository has an active Git operation

upgrade-provider left it unchanged. Complete or abort the operation manually.
To preserve a completed patch upgrade, ensure every patch is applied and the target rebase is complete, then run:
  ./scripts/upstream.sh check_in

Rerun upgrade-provider after check_in succeeds.
To intentionally discard the interrupted work, run ./scripts/upstream.sh init -f.
That command is destructive and can discard conflict resolution and patch commits`)
	}

	branch, err := patchedGitOutput(ctx, upstreamDir, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("inspect patched upstream branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == patchCheckoutBranch {
		return "", fmt.Errorf(`the patched upstream repository is still checked out on %s

upgrade-provider left it unchanged because it cannot prove whether checkout or rebase completed.
To preserve the work:
  1. Ensure every patch has been applied.
  2. If needed, run ./scripts/upstream.sh rebase -o %s and complete the rebase.
  3. Run ./scripts/upstream.sh check_in.
  4. Rerun upgrade-provider.

To intentionally discard the interrupted work, run ./scripts/upstream.sh init -f.
That command is destructive and can discard conflict resolution and patch commits`, patchCheckoutBranch, targetRef)
	}
	if branch != "" {
		return "", fmt.Errorf(`the patched upstream repository is checked out on unexpected branch %q

upgrade-provider left it unchanged. Inspect the branch and return upstream to its expected detached state before rerunning`, branch)
	}

	return "ready", nil
}

func patchedProviderInitialized(upstreamDir string) (bool, error) {
	entries, err := os.ReadDir(upstreamDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read upstream directory: %w", err)
	}

	if _, err := os.Stat(filepath.Join(upstreamDir, ".git")); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect upstream Git metadata: %w", err)
	}
	if len(entries) == 0 {
		return false, nil
	}
	return false, fmt.Errorf("upstream is not an initialized Git repository but contains files")
}

func hasActivePatchedGitOperation(ctx context.Context, upstreamDir string) (bool, error) {
	gitDir, err := patchedGitOutput(ctx, upstreamDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, fmt.Errorf("resolve upstream Git directory: %w", err)
	}

	for _, name := range []string{
		"rebase-merge",
		"rebase-apply",
		"MERGE_HEAD",
		"CHERRY_PICK_HEAD",
		"REVERT_HEAD",
		"sequencer",
		"BISECT_START",
	} {
		if _, err := os.Stat(filepath.Join(strings.TrimSpace(gitDir), name)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect Git operation path %q: %w", name, err)
		}
	}
	return false, nil
}

func patchedGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
