package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const pidLockMaxAge = time.Hour

// ErrLocked is returned when another live dispatcher owns the state DB lock.
var ErrLocked = errors.New("dispatcher already running")

type pidLock struct {
	path string
	pid  int
}

func acquirePIDLock(dbPath string) (*pidLock, error) {
	if dbPath == "" || dbPath == ":memory:" {
		return nil, nil
	}
	canonicalDBPath, err := canonicalStateDBPath(dbPath)
	if err != nil {
		return nil, err
	}
	lockPath := canonicalDBPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o750); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	pid := os.Getpid()
	for {
		err := createPIDLockFile(lockPath, pid)
		if err == nil {
			return &pidLock{path: lockPath, pid: pid}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create dispatcher lock %s: %w", lockPath, err)
		}
		lockedPID, stale, staleErr := stalePIDLock(lockPath)
		if staleErr != nil {
			return nil, staleErr
		}
		if !stale {
			return nil, fmt.Errorf("%w against state.db at %s (PID %d); stop it first or remove stale lock", ErrLocked, canonicalDBPath, lockedPID)
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale dispatcher lock %s: %w", lockPath, err)
		}
	}
}

func createPIDLockFile(lockPath string, pid int) error {
	dir := filepath.Dir(lockPath)
	tmp, err := os.CreateTemp(dir, ".dispatcher-*.lock")
	if err != nil {
		return fmt.Errorf("create temp dispatcher lock: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := fmt.Fprintf(tmp, "%d\n", pid); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp dispatcher lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp dispatcher lock: %w", err)
	}
	if err := os.Link(tmpPath, lockPath); err != nil {
		return fmt.Errorf("link dispatcher lock into place: %w", err)
	}
	return nil
}

func canonicalStateDBPath(dbPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(dbPath)
	if resolvedParent, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
		return filepath.Join(resolvedParent, filepath.Base(dbPath)), nil
	}
	abs, absErr := filepath.Abs(dbPath)
	if absErr != nil {
		return "", fmt.Errorf("canonicalize state db path %s: %w", dbPath, absErr)
	}
	return abs, nil
}

func stalePIDLock(lockPath string) (pid int, stale bool, err error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("stat dispatcher lock %s: %w", lockPath, err)
	}
	data, err := os.ReadFile(lockPath) //nolint:gosec // lockPath is derived from canonical state DB path.
	if err != nil {
		return 0, false, fmt.Errorf("read dispatcher lock %s: %w", lockPath, err)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, true, nil //nolint:nilerr // malformed lock content is intentionally treated as stale.
	}
	if time.Since(info.ModTime()) > pidLockMaxAge {
		return pid, true, nil
	}
	return pid, !pidAlive(pid), nil
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (l *pidLock) release() error {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read dispatcher lock %s: %w", l.path, err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(l.pid) {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove dispatcher lock %s: %w", l.path, err)
	}
	return nil
}

func (l *pidLock) refreshLoop(ctx context.Context, interval time.Duration) {
	if l == nil || l.path == "" {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = l.refresh()
		}
	}
}

func (l *pidLock) refresh() error {
	if l == nil || l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return fmt.Errorf("read dispatcher lock %s: %w", l.path, err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(l.pid) {
		return nil
	}
	now := time.Now()
	if err := os.Chtimes(l.path, now, now); err != nil {
		return fmt.Errorf("refresh dispatcher lock %s: %w", l.path, err)
	}
	return nil
}
