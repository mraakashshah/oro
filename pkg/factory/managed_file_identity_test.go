package factory //nolint:testpackage // Exercise private managed authority readers against actual filesystem identities.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestManagedFileIdentity(t *testing.T) {
	t.Run("marker aliases are never absence or authority", testManagedMarkerAliases)
	t.Run("aliased markers preserve initialized state", testManagedMarkerNoReset)
	t.Run("observed leaf replacement fails closed", testManagedLeafReplacement)
	t.Run("ordinary leaves preserve read semantics", testManagedOrdinaryLeaves)
	t.Run("marker aliases cannot initialize state", testManagedMarkerNoInitialization)
}

type changingManagedRoot struct {
	*os.Root
	beforeOpen, afterOpen func()
	opened                *os.File
}

func (root *changingManagedRoot) OpenFile(name string, flag int, mode os.FileMode) (*os.File, error) {
	if root.beforeOpen != nil {
		root.beforeOpen()
	}
	file, err := root.Root.OpenFile(name, flag, mode)
	root.opened = file
	if root.afterOpen != nil {
		root.afterOpen()
	}
	return file, err
}

func testManagedLeafReplacement(t *testing.T) {
	t.Helper()
	for _, moment := range []string{"before open", "after open"} {
		for _, kind := range []string{"regular", "contained", "dangling", "missing", "fifo"} {
			t.Run(moment+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "artifact")
				if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				changing := &changingManagedRoot{Root: root}
				replace := func() { replaceManagedTestLeaf(t, path, kind) }
				if moment == "before open" {
					changing.beforeOpen = replace
				} else {
					changing.afterOpen = replace
				}
				file, err := openManagedRootRegular(changing, "artifact", 100, false)
				if file != nil {
					_ = file.Close()
				}
				if err == nil || errors.Is(err, os.ErrNotExist) || file != nil {
					t.Fatalf("observed replacement became a usable file or initial absence: file=%v error=%v", file, err)
				}
				if changing.opened != nil {
					if _, err := changing.opened.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("rejected opened inode leaked: %v", err)
					}
				}
			})
		}
	}
}

func replaceManagedTestLeaf(t *testing.T, path, kind string) {
	t.Helper()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	var err error
	switch kind {
	case "regular":
		// Identical contents do not make a replacement the inspected inode.
		err = os.WriteFile(path, []byte("original"), 0o600)
	case "contained":
		// Resolving this alias opens the original inode: fstat alone is insufficient.
		err = os.Symlink(filepath.Base(path)+".old", path)
	case "dangling":
		err = os.Symlink("absent", path)
	case "fifo":
		err = syscall.Mkfifo(path, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func testManagedOrdinaryLeaves(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "marker")
	if err := os.WriteFile(path, []byte(managedStateInitMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := readManagedInitializationMarker(root, "marker"); err != nil {
		t.Fatal(err)
	}
	if err := readManagedInitializationMarker(root, "absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("genuine missing marker lost absence: %v", err)
	}
	if _, err := openManagedRegular(filepath.Join(dir, "absent"), 100, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("genuine missing artifact lost absence: %v", err)
	}
	if err := os.Link(path, filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"marker", "hardlink"} {
		file, err := openManagedRegular(filepath.Join(dir, name), 100, false)
		if err != nil {
			t.Fatalf("ordinary regular inode rejected: %v", err)
		}
		_ = file.Close()
	}
	if file, err := openManagedRegular(os.DevNull, 100, false); err == nil || file != nil {
		t.Fatalf("device accepted as a managed artifact: %v", err)
	}
}

func testManagedMarkerNoInitialization(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"contained", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			dir := managedStorageRootAuthorityFixture(t, "root-marker-only")
			marker := filepath.Join(filepath.Dir(dir), ".managed", filepath.Base(dir)+".state.init")
			replaceManagedTestLeaf(t, marker, kind)
			if fresh, err := prepareManagedDatabase(dir, true); err == nil || fresh {
				t.Fatalf("aliased authority initialized fresh state: fresh=%v error=%v", fresh, err)
			}
			for _, name := range []string{"state.db", "state.init"} {
				if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected marker created %s: %v", name, err)
				}
			}
		})
	}
}

func testManagedMarkerAliases(t *testing.T) {
	t.Helper()
	for _, target := range []string{"contained", "dangling"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "contained"), []byte(managedStateInitMarker), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "marker")); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = readManagedInitializationMarker(root, "marker")
			if err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("aliased marker became authority or fresh-state absence: %v", err)
			}
		})
	}
}

func testManagedMarkerNoReset(t *testing.T) {
	t.Helper()
	for _, location := range []string{"external", "local"} {
		for _, kind := range []string{"contained", "dangling"} {
			t.Run(location+"/"+kind, func(t *testing.T) {
				dir, store := managedStorageFixture(t)
				if err := store.save(t.Context(), testSnapshot(), 0); err != nil {
					t.Fatal(err)
				}
				if err := store.close(); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(dir, "state.init")
				if location == "external" {
					marker = filepath.Join(filepath.Dir(dir), ".managed", filepath.Base(dir)+".state.init")
				}
				if err := os.Rename(marker, marker+".saved"); err != nil {
					t.Fatal(err)
				}
				target := filepath.Base(marker) + ".saved"
				if kind == "dangling" {
					target += ".absent"
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Fatal(err)
				}
				database := filepath.Join(dir, "state.db")
				before, err := os.ReadFile(database)
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := openManagedFactoryStore(t.Context(), dir)
				if reopened != nil {
					_ = reopened.close()
				}
				if err == nil {
					t.Fatal("aliased marker adopted initialized state")
				}
				after, err := os.ReadFile(database)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("rejected marker changed initialized database: %v", err)
				}
				info, err := os.Lstat(marker)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("rejected marker alias was replaced: %v", err)
				}
			})
		}
	}
}
