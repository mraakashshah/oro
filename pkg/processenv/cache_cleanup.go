package processenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrInvalidRetention reports a non-positive subprocess cache retention window.
var ErrInvalidRetention = errors.New("invalid subprocess cache retention")

// PruneOptions configures subprocess cache namespace pruning.
type PruneOptions struct {
	MaxAge time.Duration
	Now    func() time.Time
}

// PruneResult reports how many subprocess cache namespaces were removed or kept.
type PruneResult struct {
	Removed int
	Kept    int
}

// SubprocessCacheRoot returns the default root for isolated subprocess caches.
func SubprocessCacheRoot() string {
	return defaultCacheRoot()
}

// SubprocessTmpRoot returns the default root for isolated subprocess TMPDIRs.
// This is a different directory from SubprocessCacheRoot — the cache root
// lives under os.UserCacheDir, the tmp root under os.TempDir — and it needs
// its own pruning, because one namespace is created per spawned subprocess.
func SubprocessTmpRoot() string {
	return defaultTmpRoot()
}

// PruneSubprocessCache removes subprocess cache namespaces older than MaxAge.
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
