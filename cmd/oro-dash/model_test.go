package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// TestStatusBar verifies the status bar shows daemon health + worker count + aggregate stats.
func TestStatusBar(t *testing.T) {
	tests := []struct {
		name            string
		daemonHealthy   bool
		workerCount     int
		openCount       int
		inProgressCount int
		wantContains    []string
	}{
		{
			name:            "daemon offline shows offline and bead counts",
			daemonHealthy:   false,
			workerCount:     0,
			openCount:       5,
			inProgressCount: 2,
			wantContains:    []string{"offline", "5", "2"},
		},
		{
			name:            "daemon online shows worker count and stats",
			daemonHealthy:   true,
			workerCount:     3,
			openCount:       10,
			inProgressCount: 5,
			wantContains:    []string{"3", "10", "5"},
		},
		{
			name:            "no beads shows empty counts",
			daemonHealthy:   true,
			workerCount:     2,
			openCount:       0,
			inProgressCount: 0,
			wantContains:    []string{"2", "0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				daemonHealthy:   tt.daemonHealthy,
				workerCount:     tt.workerCount,
				openCount:       tt.openCount,
				inProgressCount: tt.inProgressCount,
			}

			statusBar := m.renderStatusBar()

			for _, want := range tt.wantContains {
				if !strings.Contains(statusBar, want) {
					t.Errorf("renderStatusBar() missing %q, got: %s", want, statusBar)
				}
			}

			// Verify offline is shown in red when daemon is not healthy
			if !tt.daemonHealthy && !strings.Contains(statusBar, "offline") {
				t.Errorf("renderStatusBar() should show 'offline' when daemon is unhealthy")
			}
		})
	}
}

// TestRobotMode verifies --json flag outputs valid JSON snapshot.
func TestRobotMode(t *testing.T) {
	tests := []struct {
		name    string
		beads   []Bead
		workers []WorkerStatus
		wantErr bool
	}{
		{
			name: "valid beads and workers produces valid JSON",
			beads: []Bead{
				{ID: "bead-1", Status: "open"},
				{ID: "bead-2", Status: "in_progress"},
			},
			workers: []WorkerStatus{
				{ID: "worker-1", Status: "active"},
			},
			wantErr: false,
		},
		{
			name:    "empty beads and workers produces valid JSON",
			beads:   []Bead{},
			workers: []WorkerStatus{},
			wantErr: false,
		},
		{
			name: "only beads produces valid JSON",
			beads: []Bead{
				{ID: "bead-1", Status: "open"},
			},
			workers: []WorkerStatus{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBytes, err := robotMode(tt.beads, tt.workers)
			if (err != nil) != tt.wantErr {
				t.Errorf("robotMode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			// Verify the output is valid JSON
			var result map[string]any
			if err := json.Unmarshal(jsonBytes, &result); err != nil {
				t.Errorf("robotMode() output is not valid JSON: %v\nOutput: %s", err, string(jsonBytes))
			}

			// Verify JSON contains expected fields
			if _, ok := result["beads"]; !ok {
				t.Errorf("robotMode() JSON missing 'beads' field")
			}
			if _, ok := result["workers"]; !ok {
				t.Errorf("robotMode() JSON missing 'workers' field")
			}
		})
	}
}

// TestModel_KeyboardQuit verifies that pressing 'q' or 'ctrl+c' returns tea.Quit.
func TestModel_KeyboardQuit(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{
			name: "q key quits",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")},
		},
		{
			name: "ctrl+c quits",
			msg:  tea.KeyMsg{Type: tea.KeyCtrlC},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel()
			_, cmd := m.Update(tt.msg)
			if cmd == nil {
				t.Fatal("Update() returned nil cmd, want tea.Quit")
			}
			// tea.Quit returns a special quit message; execute the cmd to verify
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Errorf("Update() cmd produced %T, want tea.QuitMsg", msg)
			}
		})
	}
}

// TestModel_ViewRenders verifies that View() returns non-empty output containing status bar info.
func TestModel_ViewRenders(t *testing.T) {
	m := Model{
		daemonHealthy: true,
		workerCount:   3,
	}

	view := m.View()
	if view == "" {
		t.Fatal("View() returned empty string, want non-empty output")
	}
	if !strings.Contains(view, "Workers") {
		t.Errorf("View() missing 'Workers', got: %s", view)
	}
}

// TestModel_BeadsMsgUpdatesModel verifies that receiving a beadsMsg updates beads and counts.
func TestModel_BeadsMsgUpdatesModel(t *testing.T) {
	m := newModel()
	beads := []protocol.Bead{
		{ID: "b-1", Title: "Fix bug", Status: "open"},
		{ID: "b-2", Title: "Add feature", Status: "in_progress"},
		{ID: "b-3", Title: "Blocked task", Status: "blocked"},
		{ID: "b-4", Title: "Another open", Status: "open"},
	}

	updated, _ := m.Update(beadsMsg(beads))
	model, ok := updated.(Model)
	if !ok {
		t.Fatal("Update() did not return Model")
	}

	if len(model.beads) != 4 {
		t.Fatalf("expected 4 beads, got %d", len(model.beads))
	}
	if model.openCount != 2 {
		t.Errorf("openCount = %d, want 2", model.openCount)
	}
	if model.inProgressCount != 1 {
		t.Errorf("inProgressCount = %d, want 1", model.inProgressCount)
	}
}

// TestModel_WorkersMsgUpdatesModel verifies that receiving a workersMsg updates workers and daemon health.
func TestModel_WorkersMsgUpdatesModel(t *testing.T) {
	m := newModel()
	workers := []WorkerStatus{
		{ID: "w-1", Status: "active"},
		{ID: "w-2", Status: "idle"},
	}

	updated, _ := m.Update(workersMsg(workers))
	model, ok := updated.(Model)
	if !ok {
		t.Fatal("Update() did not return Model")
	}

	if !model.daemonHealthy {
		t.Error("daemonHealthy should be true when workers received")
	}
	if model.workerCount != 2 {
		t.Errorf("workerCount = %d, want 2", model.workerCount)
	}
}

// TestModel_WorkersMsgNilMeansDaemonOffline verifies nil workersMsg marks daemon as offline.
func TestModel_WorkersMsgNilMeansDaemonOffline(t *testing.T) {
	m := Model{daemonHealthy: true, workerCount: 3}

	updated, _ := m.Update(workersMsg(nil))
	model, ok := updated.(Model)
	if !ok {
		t.Fatal("Update() did not return Model")
	}

	if model.daemonHealthy {
		t.Error("daemonHealthy should be false when nil workers received")
	}
	if model.workerCount != 0 {
		t.Errorf("workerCount = %d, want 0", model.workerCount)
	}
}

// TestModel_TickMsgReturnsFetchCommands verifies that tickMsg triggers data fetching.
func TestModel_TickMsgReturnsFetchCommands(t *testing.T) {
	m := newModel()
	_, cmd := m.Update(tickMsg(time.Now()))

	if cmd == nil {
		t.Fatal("tickMsg should return a non-nil command")
	}
}

// TestModel_InitReturnsFetchCommands verifies that Init triggers data fetching.
func TestModel_InitReturnsFetchCommands(t *testing.T) {
	m := newModel()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() should return a non-nil command")
	}
}

// TestStatusBar_ShowsBeadCountsWhenDaemonOffline verifies bead counts show even without daemon.
func TestStatusBar_ShowsBeadCountsWhenDaemonOffline(t *testing.T) {
	m := Model{
		daemonHealthy:   false,
		openCount:       5,
		inProgressCount: 2,
	}

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "5") {
		t.Errorf("status bar should show open count 5 when daemon offline, got: %s", bar)
	}
	if !strings.Contains(bar, "2") {
		t.Errorf("status bar should show in-progress count 2 when daemon offline, got: %s", bar)
	}
	if !strings.Contains(bar, "offline") {
		t.Errorf("status bar should still indicate daemon is offline, got: %s", bar)
	}
}
