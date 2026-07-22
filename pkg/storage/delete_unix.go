//go:build darwin || linux

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type unixTombstoneAnchor struct {
	boundary                tombstoneBoundary
	root, directory, target *os.File
}

func sameDevice(first, second fs.FileInfo) bool {
	firstStat, firstOK := first.Sys().(*syscall.Stat_t)
	secondStat, secondOK := second.Sys().(*syscall.Stat_t)
	return firstOK && secondOK && firstStat.Dev == secondStat.Dev
}

func openTombstoneAnchor(boundary tombstoneBoundary) (tombstoneAnchor, error) {
	root, err := openDirectoryNoFollow(boundary.root)
	if err != nil {
		return nil, fmt.Errorf("open scratch root: %w", err)
	}
	if err := revalidateOpenFile(root, boundary.rootInfo); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("revalidate open scratch root: %w", err)
	}
	directory, err := openDirectoryAt(root, filepath.Base(boundary.directory))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open tombstone directory: %w", err)
	}
	if err := revalidateOpenFile(directory, boundary.directoryInfo); err != nil {
		_ = directory.Close()
		_ = root.Close()
		return nil, fmt.Errorf("revalidate open tombstone directory: %w", err)
	}
	target, err := openDirectoryAt(directory, filepath.Base(boundary.tombstone))
	if err != nil {
		_ = directory.Close()
		_ = root.Close()
		return nil, fmt.Errorf("open tombstone root: %w", err)
	}
	if err := revalidateOpenFile(target, boundary.tombstoneInfo); err != nil {
		_ = target.Close()
		_ = directory.Close()
		_ = root.Close()
		return nil, fmt.Errorf("revalidate open tombstone root: %w", err)
	}
	return &unixTombstoneAnchor{boundary: boundary, root: root, directory: directory, target: target}, nil
}

func (a *unixTombstoneAnchor) removeEntry(entry tombstoneEntry, directories map[string]fs.FileInfo) error {
	relative, err := filepath.Rel(a.boundary.tombstone, entry.path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return fmt.Errorf("unsafe tombstone entry path %s", entry.path)
	}
	parent, name, err := a.openParent(relative, directories)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := revalidateAt(parent, name, entry.info); err != nil {
		return fmt.Errorf("revalidate tombstone entry %s: %w", entry.path, err)
	}
	flags := 0
	if entry.info.IsDir() {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, flags); err != nil {
		return fmt.Errorf("remove tombstone entry %s: %w", entry.path, err)
	}
	return nil
}

func (a *unixTombstoneAnchor) removeRoot() error {
	name := filepath.Base(a.boundary.tombstone)
	if err := revalidateAt(a.directory, name, a.boundary.tombstoneInfo); err != nil {
		return fmt.Errorf("revalidate tombstone root: %w", err)
	}
	if err := unix.Unlinkat(int(a.directory.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove tombstone root %s: %w", a.boundary.tombstone, err)
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(a.directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("verify tombstone removal %s: %w", a.boundary.tombstone, err)
	}
	return nil
}

func (a *unixTombstoneAnchor) close() {
	_ = a.target.Close()
	_ = a.directory.Close()
	_ = a.root.Close()
}

func (a *unixTombstoneAnchor) openParent(relative string, directories map[string]fs.FileInfo) (*os.File, string, error) {
	parts := splitRelativePath(relative)
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("empty tombstone entry path")
	}
	current, err := duplicateFile(a.target, a.boundary.tombstone)
	if err != nil {
		return nil, "", err
	}
	for index, part := range parts[:len(parts)-1] {
		next, openErr := openDirectoryAt(current, part)
		_ = current.Close()
		if openErr != nil {
			return nil, "", fmt.Errorf("open tombstone parent %s: %w", relative, openErr)
		}
		key := filepath.Join(parts[:index+1]...)
		expected, ok := directories[key]
		if !ok {
			_ = next.Close()
			return nil, "", fmt.Errorf("uncollected tombstone parent %s", key)
		}
		if statErr := revalidateOpenFile(next, expected); statErr != nil {
			_ = next.Close()
			return nil, "", fmt.Errorf("revalidate tombstone parent %s: %w", key, statErr)
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory %s: %w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func duplicateFile(file *os.File, name string) (*os.File, error) {
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate tombstone directory: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func revalidateOpenFile(file *os.File, expected fs.FileInfo) error {
	current, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect open directory: %w", err)
	}
	if !current.IsDir() || !os.SameFile(current, expected) {
		return fmt.Errorf("directory changed")
	}
	return nil
}

func revalidateAt(parent *os.File, name string, expected fs.FileInfo) error {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("inspect entry: %w", err)
	}
	expectedStat, ok := expected.Sys().(*syscall.Stat_t)
	if !ok || uint64(expectedStat.Dev) != uint64(current.Dev) || expectedStat.Ino != current.Ino {
		return fmt.Errorf("entry changed")
	}
	return nil
}

func splitRelativePath(relative string) []string {
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) {
		return nil
	}
	parts := make([]string, 0, 4)
	for clean != "." {
		directory, name := filepath.Split(clean)
		parts = append([]string{name}, parts...)
		clean = filepath.Clean(directory)
	}
	return parts
}
