package storage_test

import (
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOroHomeKnownTempRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	temporaries := []storage.HomeTemporary{
		{Path: "tmp/oro-expired.tmp", ModifiedAt: now.Add(-24*time.Hour - time.Nanosecond)},
		{Path: "tmp/oro-expired.partial", ModifiedAt: now.Add(-48 * time.Hour)},
		{Path: "tmp/oro-active.tmp", ModifiedAt: now.Add(-48 * time.Hour), Active: true},
		{Path: "tmp/oro-young.tmp", ModifiedAt: now.Add(-23 * time.Hour)},
		{Path: "tmp/oro-symlink.tmp", ModifiedAt: now.Add(-48 * time.Hour), IsSymlink: true},
		{Path: "tmp/oro-escape.tmp", ModifiedAt: now.Add(-48 * time.Hour), IsSymlink: true},
		{Path: "tmp/unknown.tmp", ModifiedAt: now.Add(-48 * time.Hour)},
		{Path: "tmp/oro-directory", ModifiedAt: now.Add(-48 * time.Hour)},
		{Path: "../tmp/oro-escape.tmp", ModifiedAt: now.Add(-48 * time.Hour)},
	}

	selected := storage.PlanOroHomeTemporaryRetention(now, temporaries)
	if got, want := selectedTemporaryPaths(selected), map[string]bool{
		"tmp/oro-expired.tmp":     true,
		"tmp/oro-expired.partial": true,
	}; !samePaths(got, want) {
		t.Errorf("selected paths = %v, want %v", got, want)
	}
}

func selectedTemporaryPaths(temporaries []storage.HomeTemporary) map[string]bool {
	paths := make(map[string]bool, len(temporaries))
	for _, temporary := range temporaries {
		paths[temporary.Path] = true
	}
	return paths
}
