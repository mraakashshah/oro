package storage_test

import (
	"fmt"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOroHomeHandoffRetention(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	handoffs := []storage.HomeHandoff{
		{Path: "handoffs/project-a/old.md", ModifiedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "handoffs/project-a/recent.md", ModifiedAt: now.Add(-29 * 24 * time.Hour)},
		{Path: "handoffs/project-b/old.md", ModifiedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "handoffs/project-b/newest.md", ModifiedAt: now.Add(-time.Hour)},
		{Path: "handoffs/project-a/malformed/extra.md", ModifiedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "handoffs//missing-project.md", ModifiedAt: now.Add(-31 * 24 * time.Hour)},
		{Path: "handoffs/project-a/not-rendered.txt", ModifiedAt: now.Add(-31 * 24 * time.Hour)},
	}
	for i := 0; i < 10; i++ {
		handoffs = append(handoffs, storage.HomeHandoff{
			Path:       fmt.Sprintf("handoffs/project-a/expired-%02d.md", i),
			ModifiedAt: now.Add(-32*24*time.Hour + time.Duration(i)*time.Hour),
		})
	}

	selected := storage.PlanOroHomeHandoffRetention(now, handoffs)
	if got, want := selectedHandoffPaths(selected), map[string]bool{
		"handoffs/project-a/expired-00.md": true,
		"handoffs/project-a/expired-01.md": true,
	}; !samePaths(got, want) {
		t.Errorf("selected paths = %v, want %v", got, want)
	}
}

func selectedHandoffPaths(handoffs []storage.HomeHandoff) map[string]bool {
	paths := make(map[string]bool, len(handoffs))
	for _, handoff := range handoffs {
		paths[handoff.Path] = true
	}
	return paths
}
