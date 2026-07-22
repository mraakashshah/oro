package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const maxTombstoneDeleteEntries = 1024

type tombstoneDeletion struct {
	beforeBytes int64
	afterBytes  int64
}

type tombstoneEntry struct {
	path string
	info fs.FileInfo
}

type tombstoneBoundary struct {
	root, directory, tombstone             string
	rootInfo, directoryInfo, tombstoneInfo fs.FileInfo
}

func (r *NamespaceRetirer) deleteTombstone(tombstone string) (tombstoneDeletion, error) {
	boundary, err := r.validateTombstoneBoundary(tombstone)
	if err != nil {
		return tombstoneDeletion{}, err
	}
	entries, beforeBytes, err := collectTombstoneEntries(tombstone, boundary.tombstoneInfo, boundary.rootInfo)
	if err != nil {
		return tombstoneDeletion{}, err
	}
	if err := removeTombstoneEntries(boundary, entries); err != nil {
		return tombstoneDeletion{}, err
	}
	if err := removeTombstoneRoot(boundary); err != nil {
		return tombstoneDeletion{}, err
	}
	return tombstoneDeletion{beforeBytes: beforeBytes, afterBytes: 0}, nil
}

func (r *NamespaceRetirer) validateTombstoneBoundary(tombstone string) (tombstoneBoundary, error) {
	rootInfo, err := safeDirectory(r.root)
	if err != nil {
		return tombstoneBoundary{}, fmt.Errorf("validate scratch root: %w", err)
	}
	directory := filepath.Join(r.root, tombstoneDirectory)
	directoryInfo, err := safeDirectory(directory)
	if err != nil {
		return tombstoneBoundary{}, fmt.Errorf("validate tombstone directory: %w", err)
	}
	if !sameDevice(rootInfo, directoryInfo) {
		return tombstoneBoundary{}, fmt.Errorf("tombstone directory is on another device")
	}
	if filepath.Clean(filepath.Dir(tombstone)) != filepath.Clean(directory) {
		return tombstoneBoundary{}, fmt.Errorf("tombstone is outside scratch root")
	}
	info, err := safeDirectory(tombstone)
	if err != nil {
		return tombstoneBoundary{}, fmt.Errorf("validate tombstone: %w", err)
	}
	if !sameDevice(rootInfo, info) {
		return tombstoneBoundary{}, fmt.Errorf("tombstone is on another device")
	}
	return tombstoneBoundary{
		root:          r.root,
		directory:     directory,
		tombstone:     tombstone,
		rootInfo:      rootInfo,
		directoryInfo: directoryInfo,
		tombstoneInfo: info,
	}, nil
}

func removeTombstoneEntries(boundary tombstoneBoundary, entries []tombstoneEntry) error {
	if err := revalidateTombstoneBoundary(boundary); err != nil {
		return err
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if err := revalidateTombstoneBoundary(boundary); err != nil {
			return err
		}
		if err := revalidateEntry(entry.path, entry.info); err != nil {
			return err
		}
		if err := os.Remove(entry.path); err != nil { //nolint:gosec // G703: entry is a revalidated direct descendant of the catalog-owned tombstone.
			return fmt.Errorf("remove tombstone entry %s: %w", entry.path, err)
		}
	}
	return nil
}

func removeTombstoneRoot(boundary tombstoneBoundary) error {
	if err := revalidateTombstoneBoundary(boundary); err != nil {
		return err
	}
	if err := os.Remove(boundary.tombstone); err != nil { //nolint:gosec // G703: tombstone is a revalidated direct child of the controlled tombstone directory.
		return fmt.Errorf("remove tombstone root %s: %w", boundary.tombstone, err)
	}
	if err := revalidateDirectory(boundary.root, boundary.rootInfo); err != nil {
		return err
	}
	if err := revalidateDirectory(boundary.directory, boundary.directoryInfo); err != nil {
		return err
	}
	if _, err := os.Lstat(boundary.tombstone); !os.IsNotExist(err) { //nolint:gosec // G703: verifies removal of the already validated tombstone path.
		return fmt.Errorf("verify tombstone removal %s: %w", boundary.tombstone, err)
	}
	return nil
}

func revalidateTombstoneBoundary(boundary tombstoneBoundary) error {
	if err := revalidateDirectory(boundary.root, boundary.rootInfo); err != nil {
		return fmt.Errorf("revalidate scratch root: %w", err)
	}
	if err := revalidateDirectory(boundary.directory, boundary.directoryInfo); err != nil {
		return fmt.Errorf("revalidate tombstone parent: %w", err)
	}
	if err := revalidateDirectory(boundary.tombstone, boundary.tombstoneInfo); err != nil {
		return fmt.Errorf("revalidate tombstone root: %w", err)
	}
	return nil
}

func collectTombstoneEntries(path string, expected, root fs.FileInfo) ([]tombstoneEntry, int64, error) {
	return collectTombstoneEntriesBounded(path, expected, root, maxTombstoneDeleteEntries)
}

func collectTombstoneEntriesBounded(path string, expected, root fs.FileInfo, remaining int) ([]tombstoneEntry, int64, error) {
	if err := revalidateDirectory(path, expected); err != nil {
		return nil, 0, err
	}
	directory, err := os.Open(path) //nolint:gosec // G703: path is a catalog-owned tombstone revalidated before and after opening.
	if err != nil {
		return nil, 0, fmt.Errorf("open tombstone directory %s: %w", path, err)
	}
	defer func() { _ = directory.Close() }()
	openedInfo, err := directory.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("inspect open tombstone directory %s: %w", path, err)
	}
	if !openedInfo.IsDir() || !os.SameFile(openedInfo, expected) {
		return nil, 0, fmt.Errorf("tombstone directory changed: %s", path)
	}

	collected := make([]tombstoneEntry, 0)
	var bytes int64
	for {
		entries, readErr := directory.ReadDir(1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, 0, fmt.Errorf("read tombstone directory %s: %w", path, readErr)
		}
		for _, entry := range entries {
			available := remaining - len(collected)
			childEntries, childBytes, err := collectTombstoneEntry(path, entry.Name(), root, available)
			if err != nil {
				return nil, 0, err
			}
			collected = append(collected, childEntries...)
			bytes += childBytes
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return collected, bytes, nil
}

func collectTombstoneEntry(parent, name string, root fs.FileInfo, remaining int) ([]tombstoneEntry, int64, error) {
	if remaining <= 0 {
		return nil, 0, fmt.Errorf("tombstone traversal exceeds %d entries", maxTombstoneDeleteEntries)
	}
	path := filepath.Join(parent, name)
	info, err := os.Lstat(path) //nolint:gosec // G703: path is a direct child yielded by a revalidated tombstone directory handle.
	if err != nil {
		return nil, 0, fmt.Errorf("inspect tombstone entry %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !sameDevice(root, info) {
		return nil, 0, fmt.Errorf("unsafe tombstone entry %s", path)
	}
	if info.IsDir() {
		nested, bytes, err := collectTombstoneEntriesBounded(path, info, root, remaining-1)
		if err != nil {
			return nil, 0, err
		}
		return append(nested, tombstoneEntry{path: path, info: info}), bytes, nil
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("unsafe tombstone entry %s", path)
	}
	return []tombstoneEntry{{path: path, info: info}}, info.Size(), nil
}

func safeDirectory(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path) //nolint:gosec // G703: callers provide only the configured scratch boundary or its validated descendants.
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a directory")
	}
	return info, nil
}

func revalidateDirectory(path string, expected fs.FileInfo) error {
	current, err := safeDirectory(path)
	if err != nil {
		return fmt.Errorf("revalidate tombstone directory %s: %w", path, err)
	}
	if !os.SameFile(current, expected) {
		return fmt.Errorf("tombstone directory changed: %s", path)
	}
	return nil
}

func revalidateEntry(path string, expected fs.FileInfo) error {
	current, err := os.Lstat(path) //nolint:gosec // G703: path was collected beneath and is revalidated against a catalog-owned tombstone.
	if err != nil {
		return fmt.Errorf("revalidate tombstone entry %s: %w", path, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, expected) {
		return fmt.Errorf("tombstone entry changed: %s", path)
	}
	return nil
}
