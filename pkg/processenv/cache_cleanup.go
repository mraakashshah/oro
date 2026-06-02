package processenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrInvalidRetention = errors.New("invalid subprocess cache retention")

type PruneOptions struct {
	MaxAge time.Duration
	Now    func() time.Time
}

type PruneResult struct {
	Removed int
	Kept    int
}

func SubprocessCacheRoot() string {
	return defaultCacheRoot()
}

func PruneSubprocessCache(root string, opts PruneOptions) (PruneResult, error) {
	if opts.MaxAge <= 0 {
		return PruneResult{}, ErrInvalidRetention
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PruneResult{}, nil
		}
		return PruneResult{}, fmt.Errorf("read subprocess cache root %s: %w", root, err)
	}

	var result PruneResult
	var removeErrs []error
	cutoff := now().Add(-opts.MaxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			result.Kept++
			continue
		}
		info, err := entry.Info()
		if err != nil {
			removeErrs = append(removeErrs, fmt.Errorf("stat subprocess cache namespace %s: %w", entry.Name(), err))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			result.Kept++
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			removeErrs = append(removeErrs, fmt.Errorf("remove subprocess cache namespace %s: %w", path, err))
			continue
		}
		result.Removed++
	}
	return result, errors.Join(removeErrs...)
}
