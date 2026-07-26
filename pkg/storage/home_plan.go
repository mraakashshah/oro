package storage

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sync/singleflight"
)

const oroHomeMaintenanceLockName = ".storage-maintenance.lock"

// oroHomeCleanupCalls coordinates process-local callers before they contend on
// the host-wide maintenance lock.
var oroHomeCleanupCalls singleflight.Group //nolint:gochecknoglobals // cleanup coalescing is process-wide by contract.

// OroHomeCleanupEntry records an allowlisted entry and its byte evidence.
type OroHomeCleanupEntry struct {
	Path        string
	Reason      RetentionClass
	BeforeBytes int64
	AfterBytes  int64
	Changed     bool
}

// OroHomeCleanupResult is the evidence produced by a dry-run or applied
// Oro-home cleanup.
type OroHomeCleanupResult struct {
	DryRun  bool
	Entries []OroHomeCleanupEntry
}

// CleanOroHome plans or applies strict allowlisted cleanup beneath home. It
// never removes unknown paths, directories, symlinks, indexes, or SQLite WALs.
// Concurrent requests for the same home and mode coalesce into one locked run.
func CleanOroHome(ctx context.Context, home string, apply bool) (OroHomeCleanupResult, error) {
	canonicalHome, err := canonicalOroHome(home)
	if err != nil {
		return OroHomeCleanupResult{}, err
	}
	key := canonicalHome
	if apply {
		key += ":apply"
	} else {
		key += ":dry-run"
	}
	value, err, _ := oroHomeCleanupCalls.Do(key, func() (any, error) {
		return cleanOroHome(ctx, canonicalHome, apply)
	})
	if err != nil {
		return OroHomeCleanupResult{}, fmt.Errorf("clean Oro home: %w", err)
	}
	result, ok := value.(OroHomeCleanupResult)
	if !ok {
		return OroHomeCleanupResult{}, fmt.Errorf("clean Oro home: unexpected result type %T", value)
	}
	return result, nil
}

func cleanOroHome(ctx context.Context, home string, apply bool) (OroHomeCleanupResult, error) {
	lock, err := AcquireMaintenanceLock(ctx, filepath.Join(home, oroHomeMaintenanceLockName))
	if err != nil {
		return OroHomeCleanupResult{}, fmt.Errorf("acquire Oro home cleanup lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	entries, err := planOroHomeEntries(home, time.Now())
	if err != nil {
		return OroHomeCleanupResult{}, err
	}
	result := OroHomeCleanupResult{DryRun: !apply, Entries: entries}
	if !apply {
		return result, nil
	}
	for index := range result.Entries {
		entry := &result.Entries[index]
		if err := removeOroHomeEntry(home, entry.Path); err != nil {
			return OroHomeCleanupResult{}, err
		}
		entry.AfterBytes = 0
		entry.Changed = true
	}
	return result, nil
}

type observedOroHomeEntry struct {
	path       string
	modifiedAt time.Time
	size       int64
	rule       RetentionClass
}

func planOroHomeEntries(home string, now time.Time) ([]OroHomeCleanupEntry, error) {
	observed, err := observeOroHomeEntries(home)
	if err != nil {
		return nil, err
	}
	selected := selectOroHomeEntries(now, observed)
	entries := make([]OroHomeCleanupEntry, 0, len(selected))
	for _, entry := range selected {
		entries = append(entries, OroHomeCleanupEntry{
			Path:        entry.path,
			Reason:      entry.rule,
			BeforeBytes: entry.size,
			AfterBytes:  entry.size,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func observeOroHomeEntries(home string) ([]observedOroHomeEntry, error) {
	entries := make([]observedOroHomeEntry, 0)
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk Oro home: %w", walkErr)
		}
		if path == home || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relativePath, err := filepath.Rel(home, path)
		if err != nil {
			return fmt.Errorf("resolve Oro home entry path: %w", err)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect Oro home entry %s: %w", relativePath, err)
		}
		rule := ClassifyOroHome(Entry{Path: filepath.ToSlash(relativePath)})
		entries = append(entries, observedOroHomeEntry{
			path:       filepath.ToSlash(relativePath),
			modifiedAt: info.ModTime(),
			size:       nonNegativeSize(info.Size()),
			rule:       rule,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("observe Oro home entries: %w", err)
	}
	return entries, nil
}

func selectOroHomeEntries(now time.Time, observed []observedOroHomeEntry) []observedOroHomeEntry {
	selected := make(map[string]struct{})
	for _, candidate := range selectOroHomeLogs(now, observed) {
		selected[candidate.path] = struct{}{}
	}
	for _, candidate := range selectOroHomeHandoffs(now, observed) {
		selected[candidate.path] = struct{}{}
	}
	for _, candidate := range selectOroHomeBackups(now, observed) {
		selected[candidate.path] = struct{}{}
	}
	for _, candidate := range selectOroHomeTemporaries(now, observed) {
		selected[candidate.path] = struct{}{}
	}

	entries := make([]observedOroHomeEntry, 0, len(selected))
	for _, candidate := range observed {
		if _, ok := selected[candidate.path]; ok {
			entries = append(entries, candidate)
		}
	}
	return entries
}

func selectOroHomeLogs(now time.Time, observed []observedOroHomeEntry) []observedOroHomeEntry {
	logs := make([]HomeLog, 0)
	byPath := make(map[string]observedOroHomeEntry)
	for _, candidate := range observed {
		if candidate.rule != RetentionLog {
			continue
		}
		logs = append(logs, HomeLog{Path: candidate.path, ModifiedAt: candidate.modifiedAt, Size: candidate.size})
		byPath[candidate.path] = candidate
	}
	return selectedOroHomeEntries(byPath, PlanOroHomeLogRetention(now, logs, nil))
}

func selectOroHomeHandoffs(now time.Time, observed []observedOroHomeEntry) []observedOroHomeEntry {
	handoffs := make([]HomeHandoff, 0)
	byPath := make(map[string]observedOroHomeEntry)
	for _, candidate := range observed {
		if candidate.rule != RetentionHandoff {
			continue
		}
		handoffs = append(handoffs, HomeHandoff{Path: candidate.path, ModifiedAt: candidate.modifiedAt})
		byPath[candidate.path] = candidate
	}
	selected := PlanOroHomeHandoffRetention(now, handoffs)
	entries := make([]observedOroHomeEntry, 0, len(selected))
	for _, handoff := range selected {
		entries = append(entries, byPath[handoff.Path])
	}
	return entries
}

func selectOroHomeBackups(now time.Time, observed []observedOroHomeEntry) []observedOroHomeEntry {
	backups := make([]HomeBackup, 0)
	byPath := make(map[string]observedOroHomeEntry)
	for _, candidate := range observed {
		if candidate.rule != RetentionBackup {
			continue
		}
		backups = append(backups, HomeBackup{Path: candidate.path, ModifiedAt: candidate.modifiedAt})
		byPath[candidate.path] = candidate
	}
	selected := PlanOroHomeBackupRetention(now, backups)
	entries := make([]observedOroHomeEntry, 0, len(selected))
	for _, backup := range selected {
		entries = append(entries, byPath[backup.Path])
	}
	return entries
}

func selectOroHomeTemporaries(now time.Time, observed []observedOroHomeEntry) []observedOroHomeEntry {
	temporaries := make([]HomeTemporary, 0)
	byPath := make(map[string]observedOroHomeEntry)
	for _, candidate := range observed {
		if candidate.rule != RetentionTemporary {
			continue
		}
		temporaries = append(temporaries, HomeTemporary{Path: candidate.path, ModifiedAt: candidate.modifiedAt})
		byPath[candidate.path] = candidate
	}
	selected := PlanOroHomeTemporaryRetention(now, temporaries)
	entries := make([]observedOroHomeEntry, 0, len(selected))
	for _, temporary := range selected {
		entries = append(entries, byPath[temporary.Path])
	}
	return entries
}

func selectedOroHomeEntries(byPath map[string]observedOroHomeEntry, logs []HomeLog) []observedOroHomeEntry {
	entries := make([]observedOroHomeEntry, 0, len(logs))
	for _, log := range logs {
		entries = append(entries, byPath[log.Path])
	}
	return entries
}

func removeOroHomeEntry(home, relativePath string) error {
	path := filepath.Join(home, relativePath)
	validatedPath, err := validateOroHomeEntryPath(home, path)
	if err != nil {
		return err
	}
	if err := os.Remove(validatedPath); err != nil {
		return fmt.Errorf("remove Oro home entry %s: %w", relativePath, err)
	}
	return nil
}

func validateOroHomeEntryPath(home, candidate string) (string, error) {
	relativePath, err := filepath.Rel(home, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve Oro home deletion path: %w", err)
	}
	if ClassifyOroHome(Entry{Path: filepath.ToSlash(relativePath)}) == RetentionPreserve {
		return "", fmt.Errorf("refuse non-allowlisted Oro home deletion: %s", relativePath)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", fmt.Errorf("revalidate Oro home entry %s: %w", relativePath, err)
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refuse unsafe Oro home deletion: %s", relativePath)
	}
	return candidate, nil
}

func canonicalOroHome(home string) (string, error) {
	canonical, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve Oro home: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect Oro home: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("oro home is not a directory: %s", canonical)
	}
	return canonical, nil
}
