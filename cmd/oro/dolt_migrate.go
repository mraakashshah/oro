package main

import (
	"fmt"
	"os"
)

// atomicWriteFile writes content to a temporary file, fsyncs it, and atomically renames
// it to the target path. If the rename fails, the temporary file is cleaned up.
// If the parent directory does not exist, an error is returned.
// nolint:unparam // mode parameter provides flexibility for future use cases
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	tmpPath := path + ".tmp"

	// Write to temporary file
	// nolint:gosec // path is controlled by the function caller, not external input
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("failed to open tmp file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	_, err = f.Write(content)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write to tmp file: %w", err)
	}

	// Fsync before rename to ensure data durability
	err = f.Sync()
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync tmp file: %w", err)
	}

	_ = f.Close()

	// Atomically rename tmp file to target path
	err = os.Rename(tmpPath, path)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename tmp file to target path: %w", err)
	}

	return nil
}
