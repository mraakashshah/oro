package storage

import (
	"os"
	"path/filepath"
)

// NPXCleanupCandidate returns a planner candidate for an npx transient cache.
// It proves ownership only for a real, canonical _npx directory immediately
// below the resolved npm cache root. Every uncertain path is preserved.
//
//oro:testonly — scheduled developer-tool maintenance wires this candidate into production.
func NPXCleanupCandidate(npmCacheRoot, target string, leaseActive bool) Candidate {
	candidate := Candidate{Path: target, Scope: ScopeDevTools, LeaseActive: leaseActive}
	root, ok := canonicalRealDirectory(npmCacheRoot)
	if !ok {
		return candidate
	}

	resolvedTarget, ok := canonicalRealDirectory(target)
	if !ok || resolvedTarget != filepath.Join(root, "_npx") {
		return candidate
	}

	candidate.Path = resolvedTarget
	candidate.Allowlisted = true
	candidate.Owned = true
	return candidate
}

func canonicalRealDirectory(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	return filepath.Clean(resolved), true
}
