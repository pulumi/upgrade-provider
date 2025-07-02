package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type runArgs struct {
	folder string
	cmd    []string
}

func runCmd(args runArgs) error {
	cm := exec.Command(args.cmd[0], args.cmd[1:]...)
	cm.Stdout = os.Stdout
	cm.Stderr = os.Stderr
	cm.Dir = args.folder
	err := cm.Run()
	return err
}

func runCmdWithOutput(t *testing.T, args runArgs) string {
	cm := exec.Command(args.cmd[0], args.cmd[1:]...)
	buf := bytes.Buffer{}
	cm.Stdout = &buf
	cm.Stderr = &buf
	cm.Dir = args.folder
	err := cm.Run()
	require.NoError(t, err)
	return buf.String()
}

func runCmdT(t *testing.T, args runArgs) {
	err := runCmd(args)
	require.NoError(t, err)
}

func tempDir(t *testing.T, providerName string) string {
	folder := t.TempDir()
	providerFolder := filepath.Join(folder, providerName)
	err := os.MkdirAll(providerFolder, 0o755)
	require.NoError(t, err)
	return providerFolder
}

func runCheckout(t *testing.T, folder string, repo string, sha string) {
	// from https://graphite.dev/guides/git-clone-specific-commit
	// git init
	// git remote add origin <repository-url>
	// git fetch origin <commit-hash>
	// git checkout FETCH_HEAD

	runCmdT(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "init"},
	})

	runCmdT(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "remote", "add", "origin", repo},
	})

	runCmdT(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "fetch", "origin", sha},
	})

	runCmdT(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "checkout", "FETCH_HEAD"},
	})
}

func upgradeProvider(t *testing.T, folder string, targetVersion string, name string) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	binPath := filepath.Join(cwd, "bin", "upgrade-provider")

	runCmdT(t, runArgs{
		folder: folder,
		cmd: []string{
			binPath,
			"--kind", "provider",
			"--target-version", targetVersion,
			"--dry-run",
			name,
		},
	})
	require.NoError(t, err)
}

func upgradeBridge(t *testing.T, folder string, targetBridgeVersion string, name string) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	binPath := filepath.Join(cwd, "bin", "upgrade-provider")

	runCmdT(t, runArgs{
		folder: folder,
		cmd: []string{
			binPath,
			"--kind", "bridge",
			"--target-bridge-version", targetBridgeVersion,
			"--dry-run",
			name,
		},
	})
	require.NoError(t, err)
}

func runProviderUpgrade(t *testing.T, folder string, providerName string, sha string, targetVersion string) {
	repo := fmt.Sprintf("git@github.com:%s.git", providerName)

	runCheckout(t, folder, repo, sha)
	upgradeProvider(t, folder, targetVersion, providerName)
}

func runBridgeUpgrade(t *testing.T, folder string, providerName string, sha string, targetBridgeVersion string) {
	repo := fmt.Sprintf("git@github.com:%s.git", providerName)

	runCheckout(t, folder, repo, sha)
	upgradeBridge(t, folder, targetBridgeVersion, providerName)
}

func TestMain(m *testing.M) {
	err := runCmd(runArgs{
		folder: ".",
		cmd:    []string{"make", "build"},
	})
	if err != nil {
		fmt.Println("Error building the binary")
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func TestGCPMinorProviderUpgrade(t *testing.T) {
	folder := tempDir(t, "pulumi-gcp")
	version := "6.42.0"
	runProviderUpgrade(t, folder, "pulumi/pulumi-gcp", "a9f15245447dd7806220110178325f00c964b278", version)

	upgradeConfig, err := os.ReadFile(filepath.Join(folder, ".upgrade-config.yml"))
	require.NoError(t, err)

	require.Contains(t, string(upgradeConfig), fmt.Sprintf("current-upstream-version: %s", version))
	// TODO: assert on upstream submodule sha
}

func TestGCPBridgeUpgrade(t *testing.T) {
	folder := tempDir(t, "pulumi-gcp")
	version := "bfbfac3b8a54f936f55f86cddf91a1b2184bc7b9"
	runBridgeUpgrade(t, folder, "pulumi/pulumi-gcp", "a9f15245447dd7806220110178325f00c964b278", version)

	upgradeConfig, err := os.ReadFile(filepath.Join(folder, ".upgrade-config.yml"))
	require.NoError(t, err)

	require.Contains(t, string(upgradeConfig), fmt.Sprintf("current-bridge-version: %s", version))
	// TODO: check go.mod for bridge version
}

func TestGCPBridgeUpgradeNoop(t *testing.T) {
	folder := tempDir(t, "pulumi-gcp")
	version := "3.110.0"
	sha := "a9f15245447dd7806220110178325f00c964b278"
	runBridgeUpgrade(t, folder, "pulumi/pulumi-gcp", sha, version)

	upgradeConfig, err := os.ReadFile(filepath.Join(folder, ".upgrade-config.yml"))
	require.NoError(t, err)

	require.Contains(t, string(upgradeConfig), fmt.Sprintf("current-bridge-version: %s", version))

	out := runCmdWithOutput(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "status", "--porcelain"},
	})
	require.Empty(t, out)

	out = runCmdWithOutput(t, runArgs{
		folder: folder,
		cmd:    []string{"git", "log", fmt.Sprintf("%s..HEAD", sha)},
	})
	require.Empty(t, out)
}
