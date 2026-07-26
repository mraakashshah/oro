package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOroHomeCleanupPlanIsAllowlisted(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	old := time.Now().Add(-8 * 24 * time.Hour)
	writeOroHomeFile(t, home, "logs/expired.log", "old log", old)
	writeOroHomeFile(t, home, "tmp/oro-failed.tmp", "temporary", old)
	writeOroHomeFile(t, home, "index.db", "index", old)
	writeOroHomeFile(t, home, "config.yaml", "config", old)
	writeOroHomeFile(t, home, "unknown/keep.txt", "unknown", old)

	dryRun, err := storage.CleanOroHome(context.Background(), home, false)
	if err != nil {
		t.Fatalf("CleanOroHome(dry-run): %v", err)
	}
	if !dryRun.DryRun {
		t.Fatal("dry-run result did not identify dry-run mode")
	}
	assertOroHomePaths(t, dryRun.Entries, []string{"logs/expired.log", "tmp/oro-failed.tmp"})
	assertOroHomeFilesExist(t, home, "logs/expired.log", "tmp/oro-failed.tmp", "index.db", "config.yaml", "unknown/keep.txt")

	var (
		results  [2]storage.OroHomeCleanupResult
		errs     [2]error
		ready    sync.WaitGroup
		finished sync.WaitGroup
		start    = make(chan struct{})
	)
	ready.Add(2)
	finished.Add(2)
	for i := range results {
		go func(index int) {
			defer finished.Done()
			ready.Done()
			<-start
			results[index], errs[index] = storage.CleanOroHome(context.Background(), home, true)
		}(i)
	}
	ready.Wait()
	close(start)
	finished.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("CleanOroHome(apply) call %d: %v", i, err)
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("concurrent cleanup calls did not coalesce:\nfirst: %#v\nsecond: %#v", results[0], results[1])
	}
	for _, entry := range results[0].Entries {
		if !entry.Changed {
			t.Errorf("entry %q was not marked changed", entry.Path)
		}
		if entry.BeforeBytes <= 0 || entry.AfterBytes != 0 {
			t.Errorf("entry %q byte evidence = before %d, after %d; want positive before and zero after", entry.Path, entry.BeforeBytes, entry.AfterBytes)
		}
		if entry.Reason != storage.RetentionLog && entry.Reason != storage.RetentionTemporary {
			t.Errorf("entry %q reason = %q, want explicit allowlist rule", entry.Path, entry.Reason)
		}
	}
	assertOroHomeFilesAbsent(t, home, "logs/expired.log", "tmp/oro-failed.tmp")
	assertOroHomeFilesExist(t, home, "index.db", "config.yaml", "unknown/keep.txt")
}

func writeOroHomeFile(t *testing.T, home, relativePath, contents string, modifiedAt time.Time) {
	t.Helper()
	path := filepath.Join(home, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create parent for %q: %v", relativePath, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %q: %v", relativePath, err)
	}
	if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
		t.Fatalf("set age for %q: %v", relativePath, err)
	}
}

func assertOroHomePaths(t *testing.T, entries []storage.OroHomeCleanupEntry, want []string) {
	t.Helper()
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("planned paths = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("planned paths = %v, want %v", got, want)
		}
	}
}

func assertOroHomeFilesExist(t *testing.T, home string, relativePaths ...string) {
	t.Helper()
	for _, relativePath := range relativePaths {
		if _, err := os.Lstat(filepath.Join(home, relativePath)); err != nil {
			t.Errorf("expected %q to exist: %v", relativePath, err)
		}
	}
}

func assertOroHomeFilesAbsent(t *testing.T, home string, relativePaths ...string) {
	t.Helper()
	for _, relativePath := range relativePaths {
		if _, err := os.Lstat(filepath.Join(home, relativePath)); !os.IsNotExist(err) {
			t.Errorf("expected %q to be absent, got %v", relativePath, err)
		}
	}
}
