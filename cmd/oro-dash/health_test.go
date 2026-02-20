package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
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

// TestHealthViewShowsDispatcherData verifies the full integration:
//   - HealthView renders daemon PID and state when healthData is set
//   - H keybinding in BoardView switches to HealthView
//   - fetchHealth sends health directive via UDS and populates HealthData
func TestHealthViewShowsDispatcherData(t *testing.T) {
	t.Run("renders daemon PID and state from healthData", func(t *testing.T) {
		m := newModel()
		m.activeView = HealthView
		m.healthData = &HealthData{
			DaemonPID:     55001,
			DaemonState:   "running",
			ArchitectPane: PaneHealth{Name: "architect", Alive: false},
			ManagerPane:   PaneHealth{Name: "manager", Alive: false},
			WorkerCount:   2,
		}

		view := m.View()

		if !strings.Contains(view, "55001") {
			t.Errorf("HealthView missing daemon PID 55001, got:\n%s", view)
		}
		if !strings.Contains(view, "running") {
			t.Errorf("HealthView missing daemon state 'running', got:\n%s", view)
		}
	})

	t.Run("H keybinding in BoardView switches to HealthView", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})

		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}
		if model.activeView != HealthView {
			t.Errorf("expected HealthView after H key, got %v", model.activeView)
		}
	})

	t.Run("fetchHealth returns nil nil for missing socket", func(t *testing.T) {
		hd, err := fetchHealth(context.Background(), "/nonexistent/path/health.sock")
		if err != nil {
			t.Errorf("expected nil error for missing socket, got: %v", err)
		}
		if hd != nil {
			t.Errorf("expected nil HealthData for missing socket, got: %+v", hd)
		}
	})

	t.Run("fetchHealth returns HealthData from dispatcher", func(t *testing.T) {
		// Use os.TempDir() directly with a short name to avoid Unix 108-char socket path limit.
		sockPath := os.TempDir() + "/hth.sock"
		_ = os.Remove(sockPath)
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		healthJSON := `{"daemon":{"pid":99123,"uptime_seconds":42.5,"state":"running"},"architect_pane":{"name":"architect","alive":false},"manager_pane":{"name":"manager","alive":false},"workers":[]}`

		ready := make(chan struct{})
		go runMockHealthDispatcher(t, sockPath, healthJSON, ready)
		<-ready

		hd, err := fetchHealth(context.Background(), sockPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hd == nil {
			t.Fatal("expected non-nil HealthData from dispatcher")
		}
		if hd.DaemonPID != 99123 {
			t.Errorf("DaemonPID = %d, want 99123", hd.DaemonPID)
		}
		if hd.DaemonState != "running" {
			t.Errorf("DaemonState = %q, want %q", hd.DaemonState, "running")
		}
		if hd.ArchitectPane.Name != "architect" {
			t.Errorf("ArchitectPane.Name = %q, want %q", hd.ArchitectPane.Name, "architect")
		}
		// pane alive hardcoded false by dispatcher — honest display
		if hd.ArchitectPane.Alive {
			t.Errorf("ArchitectPane.Alive = true, want false (honest display)")
		}
	})
}

// runMockHealthDispatcher starts a UDS listener that accepts one connection,
// reads a DIRECTIVE message with op=health, and responds with healthJSON.
func runMockHealthDispatcher(t *testing.T, sockPath, healthJSON string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock health dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	close(ready)

	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		return
	}

	if msg.Type != protocol.MsgDirective || msg.Directive == nil || msg.Directive.Op != "health" {
		return
	}

	ack := protocol.Message{
		Type: protocol.MsgACK,
		ACK: &protocol.ACKPayload{
			OK:     true,
			Detail: healthJSON,
		},
	}
	data, _ := json.Marshal(ack)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}
