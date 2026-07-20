package storage_test

import (
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOroHomeLogRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	registry := storage.NewActiveLogRegistry()
	releaseWorker := registry.Register("logs/worker-active.log")
	t.Cleanup(releaseWorker)

	logs := []storage.HomeLog{
		{Path: "logs/worker-active.log", ModifiedAt: now.Add(-30 * 24 * time.Hour), Size: 1024 << 20},
		{Path: "logs/hooks/expired.log", ModifiedAt: now.Add(-8 * 24 * time.Hour), Size: 128 << 20},
		{Path: "logs/oldest-current.log", ModifiedAt: now.Add(-3 * time.Hour), Size: 300 << 20},
		{Path: "logs/middle-current.log", ModifiedAt: now.Add(-2 * time.Hour), Size: 300 << 20},
		{Path: "logs/newest-current.log", ModifiedAt: now.Add(-time.Hour), Size: 300 << 20},
		{Path: "indexes/not-a-log.db", ModifiedAt: now.Add(-30 * 24 * time.Hour), Size: 1024 << 20},
	}

	selected := storage.PlanOroHomeLogRetention(now, logs, registry)
	if got, want := selectedPaths(selected), map[string]bool{
		"logs/hooks/expired.log":  true,
		"logs/oldest-current.log": true,
		"logs/middle-current.log": true,
	}; !samePaths(got, want) {
		t.Errorf("selected paths = %v, want %v", got, want)
	}
}

func selectedPaths(logs []storage.HomeLog) map[string]bool {
	paths := make(map[string]bool, len(logs))
	for _, log := range logs {
		paths[log.Path] = true
	}
	return paths
}

func samePaths(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for path := range want {
		if !got[path] {
			return false
		}
	}
	return true
}
