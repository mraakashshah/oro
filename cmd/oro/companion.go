package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// discoverCompanionFrom looks for a companion binary (e.g. "oro-dash") as a
// sibling of basePath, falling back to PATH lookup via exec.LookPath.
// basePath should be the resolved (symlink-free) path of the running executable.
func discoverCompanionFrom(basePath, name string) (string, error) {
	dir := filepath.Dir(basePath)
	siblingPath := filepath.Join(dir, name)

	if _, err := os.Stat(siblingPath); err == nil {
		return siblingPath, nil
	}

	// Sibling not found — fall back to PATH lookup.
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return "", fmt.Errorf(
		"companion binary %q not found: checked %s and PATH — install via 'curl -sSL ... | bash' or build from source",
		name, siblingPath,
	)
}

// discoverCompanion finds a companion binary by looking for it as a sibling of
// the running executable, then falling back to PATH. This supports
// release-installed users who don't have the source tree.
//
//nolint:unused // wired into callers by bead oro-g6sx.7
func discoverCompanion(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		p, lookErr := exec.LookPath(name)
		if lookErr != nil {
			return "", fmt.Errorf("find companion %q via PATH: %w", name, lookErr)
		}
		return p, nil
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		p, lookErr := exec.LookPath(name)
		if lookErr != nil {
			return "", fmt.Errorf("find companion %q via PATH: %w", name, lookErr)
		}
		return p, nil
	}
	return discoverCompanionFrom(resolved, name)
}
