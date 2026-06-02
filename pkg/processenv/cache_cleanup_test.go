package processenv_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/processenv"
)

func TestPruneSubprocessCacheRemovesOnlyExpiredNamespaces(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	oldDir := filepath.Join(root, "old-namespace")
	recentDir := filepath.Join(root, "recent-namespace")
	if err := os.MkdirAll(filepath.Join(oldDir, "go-build"), 0o755); err != nil {
		t.Fatalf("mkdir old namespace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(recentDir, "go-build"), 0o755); err != nil {
		t.Fatalf("mkdir recent namespace: %v", err)
	}
	oldTime := now.Add(-8 * 24 * time.Hour)
	recentTime := now.Add(-2 * 24 * time.Hour)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old namespace: %v", err)
	}
	if err := os.Chtimes(recentDir, recentTime, recentTime); err != nil {
		t.Fatalf("chtimes recent namespace: %v", err)
	}

	result, err := processenv.PruneSubprocessCache(root, processenv.PruneOptions{
		MaxAge: 7 * 24 * time.Hour,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("PruneSubprocessCache returned error: %v", err)
	}
	if result.Removed != 1 || result.Kept != 1 {
		t.Fatalf("PruneSubprocessCache result = %+v, want removed=1 kept=1", result)
	}
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old namespace still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Fatalf("recent namespace was not preserved: %v", err)
	}

	missingRoot := filepath.Join(root, "missing")
	result, err = processenv.PruneSubprocessCache(missingRoot, processenv.PruneOptions{
		MaxAge: 7 * 24 * time.Hour,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("missing root returned error: %v", err)
	}
	if result.Removed != 0 || result.Kept != 0 {
		t.Fatalf("missing root result = %+v, want zero result", result)
	}

	_, err = processenv.PruneSubprocessCache(root, processenv.PruneOptions{
		MaxAge: 0,
		Now:    func() time.Time { return now },
	})
	if !errors.Is(err, processenv.ErrInvalidRetention) {
		t.Fatalf("MaxAge=0 error = %v, want ErrInvalidRetention", err)
	}
}
