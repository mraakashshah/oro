// Package evidencefs provides symlink-safe access to assignment evidence.
package evidencefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	privateDirMode  = 0o700
	privateFileMode = 0o600
)

// AssignmentFile identifies a canonical assignment evidence file.
type AssignmentFile struct {
	BeadID       string
	AssignmentID int64
	ModTime      time.Time
}

type directoryOperations interface {
	open(path string, flags int, mode uint32) (int, error)
	openat(parentFD int, name string, flags int, mode uint32) (int, error)
	mkdirat(parentFD int, name string, mode uint32) error
	fchmod(fd int, mode uint32) error
	fsync(fd int) error
	close(fd int) error
}

type unixDirectoryOperations struct{}

func (unixDirectoryOperations) open(path string, flags int, mode uint32) (int, error) {
	fd, err := unix.Open(path, flags, mode)
	if err != nil {
		return -1, fmt.Errorf("open directory: %w", err)
	}
	return fd, nil
}

func (unixDirectoryOperations) openat(parentFD int, name string, flags int, mode uint32) (int, error) {
	fd, err := unix.Openat(parentFD, name, flags, mode)
	if err != nil {
		return -1, fmt.Errorf("open directory relative to parent: %w", err)
	}
	return fd, nil
}

func (unixDirectoryOperations) mkdirat(parentFD int, name string, mode uint32) error {
	if err := unix.Mkdirat(parentFD, name, mode); err != nil {
		return fmt.Errorf("create directory relative to parent: %w", err)
	}
	return nil
}

func (unixDirectoryOperations) fchmod(fd int, mode uint32) error {
	if err := unix.Fchmod(fd, mode); err != nil {
		return fmt.Errorf("chmod directory: %w", err)
	}
	return nil
}

func (unixDirectoryOperations) fsync(fd int) error {
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func (unixDirectoryOperations) close(fd int) error {
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close directory: %w", err)
	}
	return nil
}

// WriteFile atomically publishes data below root without following symlinks.
func WriteFile(root string, parents []string, name string, data []byte) error {
	dirFD, err := openEvidenceRoot(root, true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, parent := range parents {
		nextFD, openErr := openEvidenceDir(dirFD, parent, true)
		if openErr != nil {
			return openErr
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	if !safeComponent(name) {
		return errors.New("evidence filename is not a safe path component")
	}
	return writeAndPublishFile(dirFD, name, data)
}

func writeAndPublishFile(dirFD int, name string, data []byte) error {
	tmpName, err := temporaryName()
	if err != nil {
		return err
	}
	fd, err := unix.Openat(dirFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, privateFileMode)
	if err != nil {
		return fmt.Errorf("create evidence temporary file: %w", err)
	}
	defer func() { _ = unix.Unlinkat(dirFD, tmpName, 0) }()
	file := os.NewFile(uintptr(fd), tmpName)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create evidence file handle")
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure evidence temporary file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write evidence: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync evidence: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close evidence: %w", err)
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, name); err != nil {
		return fmt.Errorf("publish evidence: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync evidence directory: %w", err)
	}
	return nil
}

// ReadFile reads a regular owner-only file below root without following symlinks.
func ReadFile(root string, parents []string, name string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("evidence read limit must be positive")
	}
	dirFD, err := openEvidenceRoot(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, parent := range parents {
		nextFD, openErr := openEvidenceDir(dirFD, parent, false)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	if !safeComponent(name) {
		return nil, errors.New("evidence filename is not a safe path component")
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open evidence file: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create evidence file handle")
	}
	defer func() { _ = file.Close() }()
	if err := requirePrivateRegularFile(fd); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read evidence: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("evidence exceeds read limit")
	}
	return data, nil
}

// ListAssignmentFiles lists root/<bead>/<assignment>/<name> files without
// following symlinks. Non-canonical and inaccessible entries are ignored.
func ListAssignmentFiles(root, name string) ([]AssignmentFile, error) {
	if !safeComponent(name) {
		return nil, errors.New("evidence filename is not a safe path component")
	}
	rootFD, err := openEvidenceRoot(root, false)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()

	beads, err := readDirectoryEntries(rootFD)
	if err != nil {
		return nil, err
	}
	files := make([]AssignmentFile, 0)
	for _, bead := range beads {
		if !safeComponent(bead.Name()) || !bead.IsDir() {
			continue
		}
		files = append(files, listAssignmentFilesForBead(rootFD, bead.Name(), name)...)
	}
	return files, nil
}

func listAssignmentFilesForBead(rootFD int, beadID, name string) []AssignmentFile {
	beadFD, err := openEvidenceDir(rootFD, beadID, false)
	if err != nil {
		return nil
	}
	defer func() { _ = unix.Close(beadFD) }()
	assignments, err := readDirectoryEntries(beadFD)
	if err != nil {
		return nil
	}
	files := make([]AssignmentFile, 0, len(assignments))
	for _, assignment := range assignments {
		assignmentID, parseErr := strconv.ParseInt(assignment.Name(), 10, 64)
		if parseErr != nil || assignmentID <= 0 || strconv.FormatInt(assignmentID, 10) != assignment.Name() || !assignment.IsDir() {
			continue
		}
		assignmentFD, openErr := openEvidenceDir(beadFD, assignment.Name(), false)
		if openErr != nil {
			continue
		}
		modTime, statErr := regularFileModTime(assignmentFD, name)
		_ = unix.Close(assignmentFD)
		if statErr == nil {
			files = append(files, AssignmentFile{BeadID: beadID, AssignmentID: assignmentID, ModTime: modTime})
		}
	}
	return files
}

func readDirectoryEntries(fd int) ([]os.DirEntry, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate evidence directory: %w", err)
	}
	directory := os.NewFile(uintptr(duplicate), "evidence-directory")
	if directory == nil {
		_ = unix.Close(duplicate)
		return nil, errors.New("create evidence directory handle")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list evidence directory: %w", err)
	}
	return entries, nil
}

func regularFileModTime(dirFD int, name string) (time.Time, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return time.Time{}, fmt.Errorf("open evidence file: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return time.Time{}, errors.New("create evidence file handle")
	}
	defer func() { _ = file.Close() }()
	if err := requirePrivateRegularFile(fd); err != nil {
		return time.Time{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return time.Time{}, fmt.Errorf("stat evidence file: %w", err)
	}
	return info.ModTime(), nil
}

// RemoveFile unlinks a file below root without following symlinked parents.
func RemoveFile(root string, parents []string, name string) error {
	dirFD, err := openEvidenceRoot(root, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, parent := range parents {
		nextFD, openErr := openEvidenceDir(dirFD, parent, false)
		if openErr != nil {
			return openErr
		}
		_ = unix.Close(dirFD)
		dirFD = nextFD
	}
	if !safeComponent(name) {
		return errors.New("evidence filename is not a safe path component")
	}
	if err := unix.Unlinkat(dirFD, name, 0); err != nil {
		return fmt.Errorf("remove evidence file: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync evidence directory after removal: %w", err)
	}
	return nil
}

func openEvidenceRoot(root string, create bool) (int, error) {
	return openEvidenceRootWithOps(root, create, unixDirectoryOperations{})
}

func openEvidenceRootWithOps(root string, create bool, ops directoryOperations) (int, error) {
	if !filepath.IsAbs(root) {
		return -1, errors.New("evidence root must be absolute")
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == string(filepath.Separator) {
		return -1, errors.New("evidence root must not be the filesystem root")
	}
	anchor, components, err := trustedEvidenceRootTraversal(cleanRoot)
	if err != nil {
		return -1, err
	}
	fd, err := ops.open(anchor, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open evidence root parent: %w", err)
	}
	for _, component := range components {
		nextFD, openErr := openRootComponentWithOps(fd, component, create, ops)
		if openErr != nil {
			_ = ops.close(fd)
			return -1, openErr
		}
		_ = ops.close(fd)
		fd = nextFD
	}
	if create {
		if err := ops.fchmod(fd, privateDirMode); err != nil {
			_ = ops.close(fd)
			return -1, fmt.Errorf("secure evidence root: %w", err)
		}
	} else if err := requirePrivateDirectory(fd); err != nil {
		_ = ops.close(fd)
		return -1, err
	}
	return fd, nil
}

func trustedEvidenceRootTraversal(root string) (anchor string, components []string, err error) {
	temporaryRoot := filepath.Clean(os.TempDir())
	if relative, err := filepath.Rel(temporaryRoot, root); err == nil && confinedRelativePath(relative) {
		resolvedTemporaryRoot, resolveErr := filepath.EvalSymlinks(temporaryRoot)
		if resolveErr != nil {
			return "", nil, fmt.Errorf("resolve trusted temporary root: %w", resolveErr)
		}
		return resolvedTemporaryRoot, splitRelativePath(relative), nil
	}
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)
	relative, err := filepath.Rel(volumeRoot, root)
	if err != nil || !confinedRelativePath(relative) {
		return "", nil, errors.New("evidence root cannot be confined to its volume")
	}
	return volumeRoot, splitRelativePath(relative), nil
}

func confinedRelativePath(path string) bool {
	return path != "" && path != "." && path != ".." && !filepath.IsAbs(path) &&
		!strings.HasPrefix(path, ".."+string(filepath.Separator))
}

func splitRelativePath(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}

func openEvidenceDir(parentFD int, name string, create bool) (int, error) {
	return openEvidenceDirWithOps(parentFD, name, create, unixDirectoryOperations{})
}

func openEvidenceDirWithOps(parentFD int, name string, create bool, ops directoryOperations) (int, error) {
	if !safeComponent(name) {
		return -1, errors.New("evidence directory is not a safe path component")
	}
	if create {
		if err := createDirectoryEntry(parentFD, name, "evidence directory", ops); err != nil {
			return -1, err
		}
	}
	fd, err := ops.openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open evidence directory %q: %w", name, err)
	}
	if create {
		if err := ops.fchmod(fd, privateDirMode); err != nil {
			_ = ops.close(fd)
			return -1, fmt.Errorf("secure evidence directory %q: %w", name, err)
		}
	} else if err := requirePrivateDirectory(fd); err != nil {
		_ = ops.close(fd)
		return -1, err
	}
	return fd, nil
}

func openRootComponentWithOps(parentFD int, name string, create bool, ops directoryOperations) (int, error) {
	if !safeComponent(name) {
		return -1, errors.New("evidence root contains an unsafe path component")
	}
	if create {
		if err := createDirectoryEntry(parentFD, name, "evidence root component", ops); err != nil {
			return -1, err
		}
	}
	fd, err := ops.openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open evidence root component %q: %w", name, err)
	}
	return fd, nil
}

func createDirectoryEntry(parentFD int, name, kind string, ops directoryOperations) error {
	if err := ops.mkdirat(parentFD, name, privateDirMode); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("create %s %q: %w", kind, name, err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return fmt.Errorf("sync %s parent for %q: %w", kind, name, err)
	}
	return nil
}

func safeComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

func temporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate evidence temporary filename: %w", err)
	}
	return ".qg-evidence-" + hex.EncodeToString(random[:]), nil
}

func requirePrivateDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat evidence directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o077 != 0 {
		return errors.New("evidence directory is not owner-only")
	}
	return nil
}

func requirePrivateRegularFile(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat evidence file: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		return errors.New("evidence file is not an owner-only regular file")
	}
	return nil
}
