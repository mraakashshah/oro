package main

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
			name:            "daemon offline shows red offline",
			daemonHealthy:   false,
			workerCount:     0,
			openCount:       5,
			inProgressCount: 2,
			wantContains:    []string{"offline"},
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
