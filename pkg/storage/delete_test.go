package storage //nolint:testpackage // white-box coverage verifies the guarded deletion primitive.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTombstonedDeleteRejectsPathEscape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	namespace := "0123456789abcdef0123456789abcdef"
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	t.Run("deletes only the tombstoned namespace and records byte evidence", func(t *testing.T) {
		root := t.TempDir()
		sharedCache := filepath.Join(t.TempDir(), "shared-cache")
		if err := os.Mkdir(sharedCache, 0o700); err != nil {
			t.Fatalf("create shared cache: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sharedCache, "preserve"), []byte("cache"), 0o600); err != nil {
			t.Fatalf("write shared cache: %v", err)
		}
		path := filepath.Join(root, namespace)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "scratch"), []byte("scratch"), 0o600); err != nil {
			t.Fatalf("write namespace: %v", err)
		}
		seedReleasedLease(t, catalog, namespace)

		retirer := NewNamespaceRetirer(catalog, root)
		if err := retirer.Retire(ctx, namespace, RetirementPostMerge); err != nil {
			t.Fatalf("retire namespace: %v", err)
		}
		waitForRetirement(t, retirer)

		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("namespace remains after retirement: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sharedCache, "preserve")); err != nil {
			t.Fatalf("shared cache was reachable from deletion: %v", err)
		}
		tombstone, err := catalog.Tombstone(ctx, namespace)
		if err != nil {
			t.Fatalf("load tombstone: %v", err)
		}
		assertTombstoneByteEvidence(t, tombstone, int64(len("scratch")))
	})

	t.Run("rejects a root symlink escape", func(t *testing.T) {
		outside := t.TempDir()
		path := filepath.Join(outside, namespace)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create escaped namespace: %v", err)
		}
		root := filepath.Join(t.TempDir(), "oro-subprocess")
		if err := os.Symlink(outside, root); err != nil {
			t.Fatalf("symlink scratch root: %v", err)
		}
		seedReleasedLease(t, catalog, namespace)

		retirer := NewNamespaceRetirer(catalog, root)
		if err := retirer.Retire(ctx, namespace, RetirementPostMerge); err != nil {
			t.Fatalf("schedule escaped retirement: %v", err)
		}
		if err := waitForRetirementError(t, retirer); err == nil {
			t.Fatal("escaped retirement succeeded")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("escaped namespace was removed: %v", err)
		}
	})

	t.Run("rejects a symlink entry during deletion", func(t *testing.T) {
		root := t.TempDir()
		tombstone := filepath.Join(root, tombstoneDirectory, namespace)
		if err := os.MkdirAll(tombstone, 0o700); err != nil {
			t.Fatalf("create tombstone: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "preserve")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatalf("write outside fixture: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(tombstone, "escape")); err != nil {
			t.Fatalf("symlink tombstone entry: %v", err)
		}

		retirer := NewNamespaceRetirer(catalog, root)
		if _, err := retirer.deleteTombstone(tombstone); err == nil {
			t.Fatal("delete symlink tombstone error = nil")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("symlink target was removed: %v", err)
		}
	})

	t.Run("rejects a tombstone path outside the controlled directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		retirer := NewNamespaceRetirer(catalog, root)
		if _, err := retirer.deleteTombstone(outside); err == nil {
			t.Fatal("delete outside tombstone error = nil")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("outside directory was removed: %v", err)
		}
	})

	t.Run("rejects an unowned token-shaped directory", func(t *testing.T) {
		root := t.TempDir()
		foreignNamespace := "abcdef0123456789abcdef0123456789"
		path := filepath.Join(root, foreignNamespace)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create foreign namespace: %v", err)
		}
		retirer := NewNamespaceRetirer(catalog, root)
		if err := retirer.Retire(ctx, foreignNamespace, RetirementPostMerge); err == nil {
			t.Fatal("retire unowned namespace error = nil")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unowned namespace was removed: %v", err)
		}
	})

	t.Run("rejects another filesystem during traversal", func(t *testing.T) {
		tombstone := t.TempDir()
		if err := os.WriteFile(filepath.Join(tombstone, "scratch"), []byte("scratch"), 0o600); err != nil {
			t.Fatalf("write tombstone fixture: %v", err)
		}
		tombstoneInfo, err := os.Lstat(tombstone)
		if err != nil {
			t.Fatalf("inspect tombstone: %v", err)
		}
		deviceInfo, err := os.Lstat("/dev/null")
		if err != nil {
			t.Fatalf("inspect device fixture: %v", err)
		}
		if _, _, err := collectTombstoneEntries(tombstone, tombstoneInfo, deviceInfo); err == nil {
			t.Fatal("cross-device traversal error = nil")
		}
	})

	t.Run("rejects traversal above the entry bound", func(t *testing.T) {
		tombstone := t.TempDir()
		for index := 0; index <= maxTombstoneDeleteEntries; index++ {
			name := filepath.Join(tombstone, fmt.Sprintf("scratch-%04d", index))
			if err := os.WriteFile(name, nil, 0o600); err != nil {
				t.Fatalf("write bounded traversal fixture: %v", err)
			}
		}
		tombstoneInfo, err := os.Lstat(tombstone)
		if err != nil {
			t.Fatalf("inspect bounded traversal fixture: %v", err)
		}
		if _, _, err := collectTombstoneEntries(tombstone, tombstoneInfo, tombstoneInfo); err == nil {
			t.Fatal("oversized traversal error = nil")
		}
		entries, err := os.ReadDir(tombstone)
		if err != nil {
			t.Fatalf("read preserved traversal fixture: %v", err)
		}
		if got, want := len(entries), maxTombstoneDeleteEntries+1; got != want {
			t.Fatalf("preserved entries = %d, want %d", got, want)
		}
	})

	t.Run("rejects a replaced scratch root inode", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "oro-subprocess")
		directory := filepath.Join(root, tombstoneDirectory)
		tombstone := filepath.Join(directory, namespace)
		if err := os.MkdirAll(tombstone, 0o700); err != nil {
			t.Fatalf("create replacement fixture: %v", err)
		}
		rootInfo, err := os.Lstat(root)
		if err != nil {
			t.Fatalf("inspect scratch root: %v", err)
		}
		directoryInfo, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("inspect tombstone directory: %v", err)
		}
		tombstoneInfo, err := os.Lstat(tombstone)
		if err != nil {
			t.Fatalf("inspect tombstone: %v", err)
		}
		preserved := filepath.Join(parent, "preserved-root")
		if err := os.Rename(root, preserved); err != nil {
			t.Fatalf("replace scratch root: %v", err)
		}
		if err := os.MkdirAll(tombstone, 0o700); err != nil {
			t.Fatalf("create replacement scratch root: %v", err)
		}
		boundary := tombstoneBoundary{
			root:          root,
			directory:     directory,
			tombstone:     tombstone,
			rootInfo:      rootInfo,
			directoryInfo: directoryInfo,
			tombstoneInfo: tombstoneInfo,
		}
		if err := revalidateTombstoneBoundary(boundary); err == nil {
			t.Fatal("replaced scratch root validation error = nil")
		}
		if _, err := os.Stat(filepath.Join(preserved, tombstoneDirectory, namespace)); err != nil {
			t.Fatalf("original tombstone was not preserved: %v", err)
		}
	})
}

func TestNamespaceRetirementResumesExistingTombstone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	namespace := "0123456789abcdef0123456789abcdef"
	tombstone := filepath.Join(root, tombstoneDirectory, namespace)
	if err := os.MkdirAll(tombstone, 0o700); err != nil {
		t.Fatalf("create interrupted tombstone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tombstone, "scratch"), []byte("scratch"), 0o600); err != nil {
		t.Fatalf("write interrupted tombstone: %v", err)
	}
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	seedReleasedLease(t, catalog, namespace)

	retirer := NewNamespaceRetirer(catalog, root)
	if err := retirer.Retire(ctx, namespace, RetirementPostMerge); err != nil {
		t.Fatalf("resume interrupted retirement: %v", err)
	}
	waitForRetirement(t, retirer)

	if _, err := os.Stat(tombstone); !os.IsNotExist(err) {
		t.Fatalf("interrupted tombstone remains: %v", err)
	}
	stored, err := catalog.Tombstone(ctx, namespace)
	if err != nil {
		t.Fatalf("load resumed tombstone: %v", err)
	}
	assertTombstoneByteEvidence(t, stored, int64(len("scratch")))
}

func assertTombstoneByteEvidence(t *testing.T, tombstone Tombstone, wantBefore int64) {
	t.Helper()
	if got := tombstone.BeforeBytes; got != wantBefore {
		t.Fatalf("before bytes = %d, want %d", got, wantBefore)
	}
	if got := tombstone.AfterBytes; got != 0 {
		t.Fatalf("after bytes = %d, want 0", got)
	}
}

func waitForRetirementError(t *testing.T, retirer *NamespaceRetirer) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return retirer.Wait(ctx)
}
