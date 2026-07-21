package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrMaintenanceBusy reports that another process owns the maintenance lock.
var ErrMaintenanceBusy = errors.New("storage maintenance already running")

type maintenanceLock struct {
	file *os.File
}

// AcquireMaintenanceLock obtains an advisory, host-wide maintenance lock.
// The returned closer releases the lock; process exit releases it automatically.
//
//oro:testonly — wired into production by subsequent storage maintenance work.
func AcquireMaintenanceLock(ctx context.Context, path string) (io.Closer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire maintenance lock context: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create maintenance lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // caller selects the advisory lock location.
	if err != nil {
		return nil, fmt.Errorf("open maintenance lock: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire maintenance lock context: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrMaintenanceBusy
		}
		return nil, fmt.Errorf("acquire maintenance lock: %w", err)
	}
	return &maintenanceLock{file: file}, nil
}

// Close releases the maintenance lock.
func (l *maintenanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		_ = file.Close()
		return fmt.Errorf("release maintenance lock: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close maintenance lock: %w", err)
	}
	return nil
}
