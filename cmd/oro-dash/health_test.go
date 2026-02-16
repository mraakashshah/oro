package main

import (
	"strings"
	"testing"
)

// TestHealthViewRender verifies that the Health view renders with daemon PID,
// pane statuses, and worker count.
func TestHealthViewRender(t *testing.T) {
	m := newModel()

	// Set up health data
	m.daemonHealthy = true
	m.workerCount = 3
	m.activeView = HealthView

	// Mock health data that would come from dispatcher
	m.healthData = &HealthData{
		DaemonPID:     12345,
		DaemonState:   "running",
		ArchitectPane: PaneHealth{Name: "architect", Alive: true, LastActivity: "2026-02-16T10:00:00Z"},
		ManagerPane:   PaneHealth{Name: "manager", Alive: true, LastActivity: "2026-02-16T10:00:00Z"},
		WorkerCount:   3,
	}

	view := m.View()

	// Assert: View renders with daemon PID
	if !strings.Contains(view, "12345") {
		t.Errorf("Health view missing daemon PID, got:\n%s", view)
	}

	// Assert: View renders with pane statuses
	if !strings.Contains(view, "architect") {
		t.Errorf("Health view missing architect pane, got:\n%s", view)
	}
	if !strings.Contains(view, "manager") {
		t.Errorf("Health view missing manager pane, got:\n%s", view)
	}

	// Assert: View renders with worker count
	if !strings.Contains(view, "3") {
		t.Errorf("Health view missing worker count, got:\n%s", view)
	}
}
