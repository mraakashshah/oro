//nolint:testpackage // Exercises dispatcher-owned shell runners and their runtime lease wiring.
package dispatcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/storage"
)

func TestDispatcherCommandsUseRuntimeLease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	catalog, err := storage.OpenCatalog(ctx, catalogPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer func() { _ = catalog.Close() }()
	firstWorktree := runtimeLeaseWorktree(t)
	secondWorktree := runtimeLeaseWorktree(t)
	executor := newLeasedShellCommandExecutor(catalogPath, "dispatcher-test")

	acceptance := &ShellAcceptanceRunner{Runtime: storage.RuntimeRequest{
		Catalog: catalog,
		Env:     os.Environ(),
		Workdir: firstWorktree,
		Policy:  storage.StoragePolicy{ProjectID: "dispatcher-test", RepositoryRoot: firstWorktree},
	}}
	acceptanceOutput, passed, err := acceptance.Run(ctx, `printf '%s|%s' "$GOCACHE" "$TMPDIR"`)
	if err != nil || !passed {
		t.Fatalf("acceptance.Run() = (%q, %v, %v), want passing command", acceptanceOutput, passed, err)
	}

	qg := &ShellQGRunner{executor: executor}
	qgPassed, qgOutput, err := qg.Run(ctx, secondWorktree, true, "")
	if err != nil || !qgPassed {
		t.Fatalf("qg.Run() = (%v, %q, %v), want passing command", qgPassed, qgOutput, err)
	}

	acceptanceCache, acceptanceScratch := splitRuntimeLeaseOutput(t, acceptanceOutput)
	qgCache, qgScratch := splitRuntimeLeaseOutput(t, qgOutput)
	if acceptanceCache != qgCache {
		t.Fatalf("dispatcher cache paths differ: acceptance=%q qg=%q", acceptanceCache, qgCache)
	}
	if strings.HasPrefix(acceptanceCache, firstWorktree) || strings.HasPrefix(qgCache, secondWorktree) {
		t.Fatalf("shared cache was routed into a worktree: acceptance=%q qg=%q", acceptanceCache, qgCache)
	}
	if acceptanceScratch == qgScratch {
		t.Fatalf("dispatcher scratch paths match: %q", acceptanceScratch)
	}
	if filepath.Base(filepath.Dir(acceptanceScratch)) != "oro-subprocess" ||
		filepath.Base(filepath.Dir(qgScratch)) != "oro-subprocess" {
		t.Fatalf("scratch paths are not isolated runtime namespaces: acceptance=%q qg=%q", acceptanceScratch, qgScratch)
	}

	var released int
	if err := catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_leases WHERE released_at IS NOT NULL`).Scan(&released); err != nil {
		t.Fatalf("count released leases: %v", err)
	}
	if released != 2 {
		t.Fatalf("released leases = %d, want 2", released)
	}
}

func runtimeLeaseWorktree(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	script := filepath.Join(worktree, "scripts", "quality_gate.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o750); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf '%s|%s' \"$GOCACHE\" \"$TMPDIR\"\n"), 0o750); err != nil {
		t.Fatalf("write quality gate: %v", err)
	}
	return worktree
}

func splitRuntimeLeaseOutput(t *testing.T, output string) (string, string) {
	t.Helper()
	cache, scratch, ok := strings.Cut(strings.TrimSpace(output), "|")
	if !ok || cache == "" || scratch == "" {
		t.Fatalf("runtime output = %q, want cache|scratch", output)
	}
	return cache, scratch
}
