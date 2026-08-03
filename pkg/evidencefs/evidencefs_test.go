package evidencefs //nolint:testpackage // Directory syscall ordering requires the internal injected operations boundary.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

var errInjectedDirectorySync = errors.New("injected directory sync failure")

func TestCreatedEvidenceDirectoriesSyncContainingParentsInOrder(t *testing.T) {
	ops := newRecordingDirectoryOps()
	rootFD, err := openEvidenceRootWithOps("/evidence/root", true, ops)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	beadFD, err := openEvidenceDirWithOps(rootFD, "oro-durable", true, ops)
	if err != nil {
		t.Fatalf("create bead directory: %v", err)
	}
	assignmentFD, err := openEvidenceDirWithOps(beadFD, "41", true, ops)
	if err != nil {
		t.Fatalf("create assignment directory: %v", err)
	}
	_ = ops.close(assignmentFD)
	_ = ops.close(beadFD)
	_ = ops.close(rootFD)

	want := []string{
		"open /",
		"mkdirat / evidence",
		"fsync /",
		"openat / evidence",
		"close /",
		"mkdirat /evidence root",
		"fsync /evidence",
		"openat /evidence root",
		"close /evidence",
		"chmod /evidence/root 0700",
		"mkdirat /evidence/root oro-durable",
		"fsync /evidence/root",
		"openat /evidence/root oro-durable",
		"chmod /evidence/root/oro-durable 0700",
		"mkdirat /evidence/root/oro-durable 41",
		"fsync /evidence/root/oro-durable",
		"openat /evidence/root/oro-durable 41",
		"chmod /evidence/root/oro-durable/41 0700",
		"close /evidence/root/oro-durable/41",
		"close /evidence/root/oro-durable",
		"close /evidence/root",
	}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("directory operation order:\n got: %#v\nwant: %#v", ops.calls, want)
	}
}

func TestCreatedEvidenceDirectoryParentSyncFailureIsPropagated(t *testing.T) {
	tests := []struct {
		name string
		run  func(*recordingDirectoryOps) error
	}{
		{
			name: "root",
			run: func(ops *recordingDirectoryOps) error {
				ops.failFsyncPath = "/"
				_, err := openEvidenceRootWithOps("/evidence", true, ops)
				return err
			},
		},
		{
			name: "bead",
			run: func(ops *recordingDirectoryOps) error {
				ops.addExisting("/evidence")
				ops.failFsyncPath = "/evidence"
				_, err := openEvidenceDirWithOps(ops.fdFor("/evidence"), "oro-durable", true, ops)
				return err
			},
		},
		{
			name: "assignment",
			run: func(ops *recordingDirectoryOps) error {
				ops.addExisting("/evidence/oro-durable")
				ops.failFsyncPath = "/evidence/oro-durable"
				_, err := openEvidenceDirWithOps(ops.fdFor("/evidence/oro-durable"), "41", true, ops)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := newRecordingDirectoryOps()
			err := tc.run(ops)
			if !errors.Is(err, errInjectedDirectorySync) {
				t.Fatalf("error = %v, want injected sync failure", err)
			}
			failedSync := "fsync " + ops.failFsyncPath
			failedSyncIndex := -1
			for i, call := range ops.calls {
				if call == failedSync {
					failedSyncIndex = i
					break
				}
			}
			if failedSyncIndex < 0 {
				t.Fatalf("operations %#v do not include %q", ops.calls, failedSync)
			}
			for _, call := range ops.calls[failedSyncIndex+1:] {
				if strings.HasPrefix(call, "openat ") || strings.HasPrefix(call, "mkdirat ") {
					t.Fatalf("directory creation continued after failed parent sync: %#v", ops.calls)
				}
			}
		})
	}
}

func TestCreatedEvidenceDirectoryRetryResyncsExistingParentEntry(t *testing.T) {
	tests := []struct {
		name       string
		parentPath string
		child      string
		setup      func(*recordingDirectoryOps)
		attempt    func(*recordingDirectoryOps) (int, error)
	}{
		{
			name:       "root",
			parentPath: "/evidence",
			child:      "root",
			setup: func(ops *recordingDirectoryOps) {
				ops.addExisting("/evidence")
			},
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				return openEvidenceRootWithOps("/evidence/root", true, ops)
			},
		},
		{
			name:       "bead",
			parentPath: "/evidence",
			child:      "oro-durable",
			setup: func(ops *recordingDirectoryOps) {
				ops.addExisting("/evidence")
			},
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				return openEvidenceDirWithOps(ops.fdFor("/evidence"), "oro-durable", true, ops)
			},
		},
		{
			name:       "assignment",
			parentPath: "/evidence/oro-durable",
			child:      "41",
			setup: func(ops *recordingDirectoryOps) {
				ops.addExisting("/evidence/oro-durable")
			},
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				return openEvidenceDirWithOps(ops.fdFor("/evidence/oro-durable"), "41", true, ops)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := newRecordingDirectoryOps()
			tc.setup(ops)
			ops.failFsyncRemaining[tc.parentPath] = 1
			if _, err := tc.attempt(ops); !errors.Is(err, errInjectedDirectorySync) {
				t.Fatalf("initial creation error = %v, want injected sync failure", err)
			}
			retryStart := len(ops.calls)
			fd, err := tc.attempt(ops)
			if err != nil {
				t.Fatalf("retry creation: %v", err)
			}
			_ = ops.close(fd)
			assertOperationsInOrder(t, ops.calls[retryStart:], []string{
				"mkdirat " + tc.parentPath + " " + tc.child,
				"fsync " + tc.parentPath,
				"openat " + tc.parentPath + " " + tc.child,
			})
		})
	}
}

func TestCreatedEvidenceDirectoryRetryPropagatesRepeatedParentSyncFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parentPath string
		child      string
		attempt    func(*recordingDirectoryOps) (int, error)
	}{
		{
			name:       "root",
			parentPath: "/evidence",
			child:      "root",
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				ops.addExisting("/evidence")
				return openEvidenceRootWithOps("/evidence/root", true, ops)
			},
		},
		{
			name:       "bead",
			parentPath: "/evidence",
			child:      "oro-durable",
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				ops.addExisting("/evidence")
				return openEvidenceDirWithOps(ops.fdFor("/evidence"), "oro-durable", true, ops)
			},
		},
		{
			name:       "assignment",
			parentPath: "/evidence/oro-durable",
			child:      "41",
			attempt: func(ops *recordingDirectoryOps) (int, error) {
				ops.addExisting("/evidence/oro-durable")
				return openEvidenceDirWithOps(ops.fdFor("/evidence/oro-durable"), "41", true, ops)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := newRecordingDirectoryOps()
			ops.failFsyncRemaining[tc.parentPath] = 2
			if _, err := tc.attempt(ops); !errors.Is(err, errInjectedDirectorySync) {
				t.Fatalf("initial creation error = %v, want injected sync failure", err)
			}
			retryStart := len(ops.calls)
			if _, err := tc.attempt(ops); !errors.Is(err, errInjectedDirectorySync) {
				t.Fatalf("retry error = %v, want repeated injected sync failure", err)
			}
			retryCalls := ops.calls[retryStart:]
			assertOperationsInOrder(t, retryCalls, []string{
				"mkdirat " + tc.parentPath + " " + tc.child,
				"fsync " + tc.parentPath,
			})
			for _, call := range retryCalls {
				if call == "openat "+tc.parentPath+" "+tc.child {
					t.Fatalf("retry opened child after failed parent sync: %#v", retryCalls)
				}
			}
		})
	}
}

func TestWriteFileRetainsPrivateModesAndReadRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "review-evidence")
	want := []byte(`{"assignment_id":41}`)
	if err := WriteFile(root, []string{"oro-private", "41"}, "1.json", want); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, path := range []string{
		root,
		filepath.Join(root, "oro-private"),
		filepath.Join(root, "oro-private", "41"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory mode for %s = %04o, want 0700", path, got)
		}
	}
	filePath := filepath.Join(root, "oro-private", "41", "1.json")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat evidence file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("evidence file mode = %04o, want 0600", got)
	}
	got, err := ReadFile(root, []string{"oro-private", "41"}, "1.json", 1024)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadFile = %q, want %q", got, want)
	}
}

func TestEvidenceAccessDoesNotFollowSymlinks(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		root := filepath.Join(parent, "root")
		if err := os.Symlink(target, root); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		if err := WriteFile(root, []string{"oro-link", "1"}, "1.json", []byte("unsafe")); err == nil {
			t.Fatal("WriteFile followed evidence root symlink")
		}
	})

	t.Run("assignment directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		beadDir := filepath.Join(root, "oro-link")
		if err := os.MkdirAll(beadDir, 0o700); err != nil {
			t.Fatalf("mkdir bead: %v", err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(beadDir, "1")); err != nil {
			t.Fatalf("symlink assignment: %v", err)
		}
		if err := WriteFile(root, []string{"oro-link", "1"}, "1.json", []byte("unsafe")); err == nil {
			t.Fatal("WriteFile followed assignment directory symlink")
		}
	})

	t.Run("file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		assignmentDir := filepath.Join(root, "oro-link", "1")
		if err := os.MkdirAll(assignmentDir, 0o700); err != nil {
			t.Fatalf("mkdir assignment: %v", err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, []byte("unsafe"), 0o600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(assignmentDir, "1.json")); err != nil {
			t.Fatalf("symlink file: %v", err)
		}
		if _, err := ReadFile(root, []string{"oro-link", "1"}, "1.json", 1024); err == nil {
			t.Fatal("ReadFile followed evidence file symlink")
		}
	})
}

func TestEvidenceRootRejectsEverySymlinkedAncestor(t *testing.T) {
	components := []string{"safe", "link", "existing-root"}
	operations := []struct {
		name string
		run  func(string) error
	}{
		{
			name: "write",
			run: func(root string) error {
				return WriteFile(root, []string{"oro-link", "1"}, "1.json", []byte("new evidence"))
			},
		},
		{
			name: "read",
			run: func(root string) error {
				_, err := ReadFile(root, []string{"oro-link", "1"}, "1.json", 1024)
				return err
			},
		},
	}
	for _, operation := range operations {
		for linkIndex, linkName := range components {
			t.Run(operation.name+"/"+linkName, func(t *testing.T) {
				base := t.TempDir()
				external := t.TempDir()
				target := filepath.Join(external, "target")
				resolvedRoot := filepath.Join(append([]string{target}, components[linkIndex+1:]...)...)
				if err := os.MkdirAll(resolvedRoot, 0o700); err != nil {
					t.Fatalf("create external root: %v", err)
				}
				if operation.name == "read" {
					evidenceDir := filepath.Join(resolvedRoot, "oro-link", "1")
					if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
						t.Fatalf("create external evidence directory: %v", err)
					}
					if err := os.WriteFile(filepath.Join(evidenceDir, "1.json"), []byte("external evidence"), 0o600); err != nil {
						t.Fatalf("create external evidence: %v", err)
					}
				}
				marker := filepath.Join(external, "marker")
				if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
					t.Fatalf("create external marker: %v", err)
				}

				linkParent := filepath.Join(append([]string{base}, components[:linkIndex]...)...)
				if err := os.MkdirAll(linkParent, 0o700); err != nil {
					t.Fatalf("create safe prefix: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(linkParent, linkName)); err != nil {
					t.Fatalf("create ancestor symlink: %v", err)
				}
				root := filepath.Join(append([]string{base}, components...)...)
				before := snapshotDirectoryTree(t, external)

				if err := operation.run(root); err == nil {
					t.Fatalf("%s followed symlink at root component %q", operation.name, linkName)
				}
				after := snapshotDirectoryTree(t, external)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("external target changed:\n before: %#v\n  after: %#v", before, after)
				}
			})
		}
	}
}

func TestExistingEvidenceRootTraversesAndSyncsEveryComponent(t *testing.T) {
	ops := newRecordingDirectoryOps()
	ops.addExisting("/safe")
	ops.addExisting("/safe/link")
	ops.addExisting("/safe/link/existing-root")
	fd, err := openEvidenceRootWithOps("/safe/link/existing-root", true, ops)
	if err != nil {
		t.Fatalf("open existing evidence root: %v", err)
	}
	_ = ops.close(fd)
	want := []string{
		"open /",
		"mkdirat / safe",
		"fsync /",
		"openat / safe",
		"close /",
		"mkdirat /safe link",
		"fsync /safe",
		"openat /safe link",
		"close /safe",
		"mkdirat /safe/link existing-root",
		"fsync /safe/link",
		"openat /safe/link existing-root",
		"close /safe/link",
		"chmod /safe/link/existing-root 0700",
		"close /safe/link/existing-root",
	}
	if !reflect.DeepEqual(ops.calls, want) {
		t.Fatalf("existing-root operation order:\n got: %#v\nwant: %#v", ops.calls, want)
	}
}

func TestTrustedEvidenceRootTraversalCanonicalizesTemporaryAnchor(t *testing.T) {
	root := filepath.Join(os.TempDir(), "safe", "existing-root")
	anchor, components, err := trustedEvidenceRootTraversal(root)
	if err != nil {
		t.Fatalf("trusted evidence root traversal: %v", err)
	}
	wantAnchor, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve OS temporary root: %v", err)
	}
	if anchor != wantAnchor {
		t.Fatalf("trusted anchor = %q, want %q", anchor, wantAnchor)
	}
	if want := []string{"safe", "existing-root"}; !reflect.DeepEqual(components, want) {
		t.Fatalf("traversal components = %#v, want %#v", components, want)
	}
}

func snapshotDirectoryTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s:%s", relative, info.Mode())
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += ":" + string(contents)
		}
		snapshot = append(snapshot, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot directory tree: %v", err)
	}
	return snapshot
}

type recordingDirectoryOps struct {
	calls              []string
	nextFD             int
	pathByFD           map[int]string
	existing           map[string]bool
	failFsyncPath      string
	failFsyncRemaining map[string]int
}

func newRecordingDirectoryOps() *recordingDirectoryOps {
	ops := &recordingDirectoryOps{
		nextFD:             10,
		pathByFD:           make(map[int]string),
		existing:           map[string]bool{"/": true},
		failFsyncRemaining: make(map[string]int),
	}
	ops.addExisting("/")
	return ops
}

func (o *recordingDirectoryOps) addExisting(path string) int {
	o.existing[path] = true
	return o.fdFor(path)
}

func (o *recordingDirectoryOps) fdFor(path string) int {
	for fd, candidate := range o.pathByFD {
		if candidate == path {
			return fd
		}
	}
	o.nextFD++
	o.pathByFD[o.nextFD] = path
	return o.nextFD
}

func (o *recordingDirectoryOps) open(path string, _ int, _ uint32) (int, error) {
	o.calls = append(o.calls, "open "+path)
	if !o.existing[path] {
		return -1, unix.ENOENT
	}
	return o.fdFor(path), nil
}

func (o *recordingDirectoryOps) openat(parentFD int, name string, _ int, _ uint32) (int, error) {
	parent := o.pathByFD[parentFD]
	o.calls = append(o.calls, "openat "+parent+" "+name)
	path := directoryChild(parent, name)
	if !o.existing[path] {
		return -1, unix.ENOENT
	}
	return o.fdFor(path), nil
}

func (o *recordingDirectoryOps) mkdirat(parentFD int, name string, _ uint32) error {
	parent := o.pathByFD[parentFD]
	o.calls = append(o.calls, "mkdirat "+parent+" "+name)
	path := directoryChild(parent, name)
	if o.existing[path] {
		return unix.EEXIST
	}
	o.existing[path] = true
	return nil
}

func (o *recordingDirectoryOps) fchmod(fd int, mode uint32) error {
	o.calls = append(o.calls, fmt.Sprintf("chmod %s %04o", o.pathByFD[fd], mode))
	return nil
}

func (o *recordingDirectoryOps) fsync(fd int) error {
	path := o.pathByFD[fd]
	o.calls = append(o.calls, "fsync "+path)
	if o.failFsyncRemaining[path] > 0 {
		o.failFsyncRemaining[path]--
		return errInjectedDirectorySync
	}
	if path == o.failFsyncPath {
		return errInjectedDirectorySync
	}
	return nil
}

func assertOperationsInOrder(t *testing.T, calls, want []string) {
	t.Helper()
	wantIndex := 0
	for _, call := range calls {
		if wantIndex < len(want) && call == want[wantIndex] {
			wantIndex++
		}
	}
	if wantIndex != len(want) {
		t.Fatalf("operations %#v do not contain ordered sequence %#v", calls, want)
	}
}

func (o *recordingDirectoryOps) close(fd int) error {
	o.calls = append(o.calls, "close "+o.pathByFD[fd])
	return nil
}

func directoryChild(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}
