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
	// singleflight.Do coalesces only calls that are still IN FLIGHT. If the first
	// goroutine finishes before the second reaches Do, the second runs a fresh
	// pass, finds the allowlisted files already removed, and correctly reports no
	// entries. Both interleavings are correct, so assert the invariants that hold
	// either way instead of requiring the two results to be identical. Safety
	// against concurrent destructive runs comes from AcquireMaintenanceLock in
	// cleanOroHome, not from singleflight.
	performed := make([]storage.OroHomeCleanupResult, 0, len(results))
	for _, result := range results {
		if len(result.Entries) > 0 {
			performed = append(performed, result)
		}
	}
	if len(performed) == 0 {
		t.Fatalf("neither concurrent cleanup reported the allowlisted entries:\nfirst: %#v\nsecond: %#v", results[0], results[1])
	}
	for index, result := range performed {
		if result.DryRun {
			t.Errorf("apply result %d reported dry-run mode", index)
		}
		assertOroHomePaths(t, result.Entries, []string{"logs/expired.log", "tmp/oro-failed.tmp"})
	}
	// When the calls do overlap, singleflight shares one result, so both callers
	// must observe exactly the same evidence.
	if len(performed) == 2 && !reflect.DeepEqual(performed[0], performed[1]) {
		t.Fatalf("coalesced cleanup results differ:\nfirst: %#v\nsecond: %#v", performed[0], performed[1])
	}
	for _, entry := range performed[0].Entries {
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

func TestCleanOroHomeMutationGuards(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, home string)
	}{
		{
			name: "dry-run and apply evidence",
			run: func(t *testing.T, home string) {
				t.Helper()
				old := time.Now().Add(-8 * 24 * time.Hour)
				writeOroHomeFile(t, home, "logs/expired.log", "old log", old)
				writeOroHomeFile(t, home, "tmp/oro-failed.tmp", "temporary", old)
				writeOroHomeFile(t, home, "unknown/keep.txt", "unknown", old)

				dryRun, err := storage.CleanOroHome(context.Background(), home, false)
				if err != nil {
					t.Fatalf("dry-run: %v", err)
				}
				if !dryRun.DryRun || len(dryRun.Entries) != 2 {
					t.Fatalf("dry-run result = %#v", dryRun)
				}
				assertOroHomePaths(t, dryRun.Entries, []string{"logs/expired.log", "tmp/oro-failed.tmp"})
				assertOroHomeFilesExist(t, home, "logs/expired.log", "tmp/oro-failed.tmp", "unknown/keep.txt")

				applied, err := storage.CleanOroHome(context.Background(), home, true)
				if err != nil {
					t.Fatalf("apply: %v", err)
				}
				if applied.DryRun || len(applied.Entries) != 2 {
					t.Fatalf("apply result = %#v", applied)
				}
				for _, entry := range applied.Entries {
					if !entry.Changed || entry.BeforeBytes <= 0 || entry.AfterBytes != 0 {
						t.Errorf("entry %q evidence = %#v", entry.Path, entry)
					}
				}
				assertOroHomeFilesAbsent(t, home, "logs/expired.log", "tmp/oro-failed.tmp")
				assertOroHomeFilesExist(t, home, "unknown/keep.txt")
			},
		},
		{
			name: "canceled lock",
			run: func(t *testing.T, home string) {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := storage.CleanOroHome(ctx, home, true); err == nil {
					t.Fatal("canceled cleanup unexpectedly succeeded")
				}
			},
		},
		{
			name: "walk failure",
			run: func(t *testing.T, home string) {
				t.Helper()
				blocked := filepath.Join(home, "logs")
				if err := os.Mkdir(blocked, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(blocked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
				if _, err := storage.CleanOroHome(context.Background(), home, false); err == nil {
					t.Fatal("inaccessible child unexpectedly planned successfully")
				}
			},
		},
		{
			name: "remove failure",
			run: func(t *testing.T, home string) {
				t.Helper()
				old := time.Now().Add(-8 * 24 * time.Hour)
				logs := filepath.Join(home, "logs")
				if err := os.Mkdir(logs, 0o700); err != nil {
					t.Fatal(err)
				}
				writeOroHomeFile(t, home, "logs/expired.log", "old log", old)
				if err := os.Chmod(logs, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(logs, 0o700) })
				if _, err := storage.CleanOroHome(context.Background(), home, true); err == nil {
					t.Fatal("read-only logs directory unexpectedly allowed removal")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, t.TempDir())
		})
	}
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
