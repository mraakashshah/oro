package storage_test

import (
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOroHomeBackupRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	backups := []storage.HomeBackup{
		{Path: "backups/state.db.bak-20260720-110000", ModifiedAt: now.Add(-time.Hour)},
		{Path: "backups/state.db.bak-20260720-100000", ModifiedAt: now.Add(-2 * time.Hour)},
		{Path: "backups/state.db.bak-20260720-090000", ModifiedAt: now.Add(-3 * time.Hour)},
		{Path: "backups/state.db.bak-20260710-120000", ModifiedAt: now.Add(-10 * 24 * time.Hour)},
		{Path: "backups/state.db.bak-20260709-120000", ModifiedAt: now.Add(-11 * 24 * time.Hour)},
		{Path: "backups/state.db.bak-20260715-120000", ModifiedAt: now.Add(-5 * 24 * time.Hour)},
		{Path: "backups/other.db.bak-20260710-120000", ModifiedAt: now.Add(-10 * 24 * time.Hour)},
		{Path: "backups/other.db.bak-20260709-120000", ModifiedAt: now.Add(-11 * 24 * time.Hour)},
		{Path: "backups/other.db.bak-20260708-120000", ModifiedAt: now.Add(-12 * 24 * time.Hour)},
		{Path: "backups/other.db.bak-20260707-120000", ModifiedAt: now.Add(-13 * 24 * time.Hour)},
		{Path: "backups/state.db.bak-not-a-timestamp", ModifiedAt: now.Add(-30 * 24 * time.Hour)},
		{Path: "backups/state.sqlite.bak-20260701-120000", ModifiedAt: now.Add(-30 * 24 * time.Hour)},
		{Path: "backups/unrelated.tar", ModifiedAt: now.Add(-30 * 24 * time.Hour)},
	}

	selected := storage.PlanOroHomeBackupRetention(now, backups)
	if got, want := selectedBackupPaths(selected), map[string]bool{
		"backups/state.db.bak-20260710-120000": true,
		"backups/state.db.bak-20260709-120000": true,
		"backups/other.db.bak-20260707-120000": true,
	}; !samePaths(got, want) {
		t.Errorf("selected paths = %v, want %v", got, want)
	}
}

func selectedBackupPaths(backups []storage.HomeBackup) map[string]bool {
	paths := make(map[string]bool, len(backups))
	for _, backup := range backups {
		paths[backup.Path] = true
	}
	return paths
}
