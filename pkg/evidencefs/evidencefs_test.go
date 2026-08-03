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

type recordingDirectoryOps struct {
	calls         []string
	nextFD        int
	pathByFD      map[int]string
	existing      map[string]bool
	failFsyncPath string
}

func newRecordingDirectoryOps() *recordingDirectoryOps {
	ops := &recordingDirectoryOps{
		nextFD:   10,
		pathByFD: make(map[int]string),
		existing: map[string]bool{"/": true},
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

func (o *recordingDirectoryOps) lstat(path string) (os.FileMode, error) {
	if !o.existing[path] {
		return 0, os.ErrNotExist
	}
	return os.ModeDir | 0o700, nil
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
	if path == o.failFsyncPath {
		return errInjectedDirectorySync
	}
	return nil
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
