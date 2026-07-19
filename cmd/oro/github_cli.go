package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const minimumGitHubCLIMajor = 2

// CLIEvidence records the verified GitHub CLI used by Oro.
type CLIEvidence struct {
	Path    string
	Version string
}

// InstallDeps supplies the process dependencies used to install and attest gh.
// Tests inject these dependencies so no package manager is mutated.
type InstallDeps struct {
	GOOS     string
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
}

// EnsureManagedGitHubCLI installs GitHub CLI through Homebrew when required and
// returns evidence for a supported executable. It never mutates Homebrew when
// an existing supported CLI is available.
func EnsureManagedGitHubCLI(ctx context.Context, deps InstallDeps) (CLIEvidence, error) {
	deps = installDepsWithDefaults(deps)
	if deps.GOOS != "darwin" {
		return CLIEvidence{}, fmt.Errorf("managed GitHub CLI installation is supported on macOS; install gh with your package manager")
	}

	ghPath, err := deps.LookPath("gh")
	if err != nil {
		ghPath, err = installGitHubCLI(ctx, deps)
		if err != nil {
			return CLIEvidence{}, err
		}
	}

	return attestGitHubCLI(ctx, deps, ghPath)
}

func installDepsWithDefaults(deps InstallDeps) InstallDeps {
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	if deps.Run == nil {
		deps.Run = runGitHubCLICommand
	}
	return deps
}

func runGitHubCLICommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // executable and arguments are fixed by Oro.
	return cmd.CombinedOutput()
}

func installGitHubCLI(ctx context.Context, deps InstallDeps) (string, error) {
	brewPath, err := deps.LookPath("brew")
	if err != nil {
		return "", fmt.Errorf("GitHub CLI is missing and Homebrew is unavailable; install Homebrew, then run brew install gh: %w", err)
	}
	if _, err := deps.Run(ctx, brewPath, "install", "gh"); err != nil {
		return "", fmt.Errorf("install GitHub CLI with brew install gh: %w", err)
	}
	ghPath, err := deps.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("brew install gh completed but gh is still unavailable; ensure Homebrew's bin directory is on PATH: %w", err)
	}
	return ghPath, nil
}

func attestGitHubCLI(ctx context.Context, deps InstallDeps, path string) (CLIEvidence, error) {
	out, err := deps.Run(ctx, path, "--version")
	if err != nil {
		return CLIEvidence{}, fmt.Errorf("attest GitHub CLI at %s: %w", path, err)
	}
	version, err := supportedGitHubCLIVersion(string(out))
	if err != nil {
		return CLIEvidence{}, fmt.Errorf("GitHub CLI readiness failed at %s: %w", path, err)
	}
	return CLIEvidence{Path: path, Version: version}, nil
}

func supportedGitHubCLIVersion(output string) (string, error) {
	fields := strings.Fields(output)
	if len(fields) < 3 || fields[0] != "gh" || fields[1] != "version" {
		return "", fmt.Errorf("unsupported gh version output %q", strings.TrimSpace(output))
	}
	version := strings.TrimPrefix(fields[2], "v")
	majorText, _, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < minimumGitHubCLIMajor {
		return "", fmt.Errorf("unsupported gh version %q; require version %d or later", version, minimumGitHubCLIMajor)
	}
	return version, nil
}
