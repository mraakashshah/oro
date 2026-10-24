package factory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

func lockObjective(dir string) (func() error, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create objective directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "objective.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Explicit admitted objective state path.
	if err != nil {
		return nil, fmt.Errorf("open objective lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("objective is already running or its lock is unavailable: %w", err)
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			releaseErr = errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
		})
		if releaseErr != nil {
			return fmt.Errorf("release objective lock: %w", releaseErr)
		}
		return nil
	}, nil
}
