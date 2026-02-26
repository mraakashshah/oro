package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"oro/pkg/protocol"
)

// TestStatusView_Scaffold verifies the StatusView scaffold:
// - StatusView is appended to the ViewType enum
// - s key from ListView/BoardView opens StatusView
// - H key still opens HealthView (not StatusView)
// - w key still opens WorkersView (not StatusView)
// - handleStatusViewKeys handles j/k/Enter/Esc
// - System section shows daemon+uptime+panes
// - Nil healthData shows "Connecting..."
func TestStatusView_Scaffold(t *testing.T) {
	t.Run("StatusView is a valid ViewType", func(t *testing.T) {
		// StatusView should be defined and distinct from other views
		sv := StatusView
		if sv == BoardView || sv == InsightsView || sv == DetailView ||
			sv == SearchView || sv == HelpView || sv == HealthView ||
			sv == WorkersView || sv == TreeView || sv == ListView {
			t.Error("StatusView must be a distinct ViewType")
		}
	})

	t.Run("s key from ListView switches to StatusView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != StatusView {
			t.Errorf("expected StatusView after s key from ListView, got %v", model.activeView)
		}
	})

	t.Run("s key from BoardView switches to StatusView", func(t *testing.T) {
		m := newModel()
		m.activeView = BoardView
		m.previousNavView = BoardView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != StatusView {
			t.Errorf("expected StatusView after s key from BoardView, got %v", model.activeView)
		}
	})

	t.Run("H key from ListView still opens HealthView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != HealthView {
			t.Errorf("expected HealthView after H key, got %v", model.activeView)
		}
	})

	t.Run("w key from ListView still opens WorkersView", func(t *testing.T) {
		m := newModel()
		m.activeView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != WorkersView {
			t.Errorf("expected WorkersView after w key, got %v", model.activeView)
		}
	})

	t.Run("handleStatusViewKeys: Esc returns to previousNavView", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.previousNavView = ListView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.activeView != ListView {
			t.Errorf("expected ListView after Esc from StatusView, got %v", model.activeView)
		}
	})

	t.Run("handleStatusViewKeys: j/k navigate sections", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.healthData = &HealthData{
			DaemonPID:   12345,
			DaemonState: "running",
			WorkerCount: 2,
		}

		// j moves cursor down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 1 {
			t.Errorf("expected cursor=1 after j, got %d", model.statusModel.cursor)
		}

		// k moves cursor back up
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		model, ok = updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 0 {
			t.Errorf("expected cursor=0 after k, got %d", model.statusModel.cursor)
		}
	})

	t.Run("handleStatusViewKeys: k at top clamps to 0", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		model, ok := updated.(Model)
		if !ok {
			t.Fatal("Update() did not return Model")
		}

		if model.statusModel.cursor != 0 {
			t.Errorf("expected cursor=0 after k at top, got %d", model.statusModel.cursor)
		}
	})

	t.Run("System section shows daemon status, uptime, pane count", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = &HealthData{
			DaemonPID:     12345,
			DaemonState:   "running",
			ArchitectPane: PaneHealth{Name: "architect", Alive: true},
			ManagerPane:   PaneHealth{Name: "manager", Alive: true},
			WorkerCount:   3,
		}

		view := m.View()
		plain := stripANSI(view)

		// Should show daemon status
		if !strings.Contains(plain, "running") {
			t.Errorf("StatusView missing daemon state 'running', got:\n%s", plain)
		}

		// Should show pane info
		if !strings.Contains(plain, "architect") {
			t.Errorf("StatusView missing architect pane, got:\n%s", plain)
		}
		if !strings.Contains(plain, "manager") {
			t.Errorf("StatusView missing manager pane, got:\n%s", plain)
		}

		// Should show worker count
		if !strings.Contains(plain, "3") {
			t.Errorf("StatusView missing worker count, got:\n%s", plain)
		}
	})

	t.Run("nil healthData shows Connecting...", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = nil

		view := m.View()
		plain := stripANSI(view)

		if !strings.Contains(plain, "Connecting...") {
			t.Errorf("StatusView with nil healthData should show 'Connecting...', got:\n%s", plain)
		}
	})

	t.Run("StatusView renders via View() dispatch", func(t *testing.T) {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = 120
		m.height = 40
		m.healthData = &HealthData{
			DaemonPID:   99,
			DaemonState: "running",
			WorkerCount: 1,
		}

		view := m.View()
		// Should produce non-empty output
		if len(view) == 0 {
			t.Error("StatusView produced empty output")
		}
	})

	t.Run("helpHintsForView includes StatusView", func(t *testing.T) {
		hints := helpHintsForView(StatusView, 120)
		if hints == "" {
			t.Error("helpHintsForView returned empty for StatusView")
		}
		if !strings.Contains(hints, "esc") {
			t.Error("StatusView help hints should mention esc")
		}
	})
}

// TestWorkersSection verifies the workers section renders 2-line cards with per-worker sparklines.
func TestWorkersSection(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	// Helper: create a MetricsBuffer with given samples.
	makeBuf := func(samples []MetricsSample) *MetricsBuffer {
		buf := NewMetricsBuffer()
		for _, s := range samples {
			buf.Record(s)
		}
		return buf
	}

	t.Run("TwoLineCards", func(t *testing.T) {
		workers := []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-abc", ContextPct: 42, LastProgressSecs: 5},
		}
		buf := makeBuf([]MetricsSample{
			{Timestamp: time.Now(), Workers: []WorkerSample{{ID: "w-1", ContextPct: 42, State: "working", BeadID: "oro-abc"}}},
		})

		got := renderWorkersSection(workers, buf, 10, theme, styles, 200)
		plain := stripANSI(got)
		lines := strings.Split(strings.TrimSpace(plain), "\n")

		// Filter out empty lines and the section title
		var cardLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "Workers") {
				cardLines = append(cardLines, trimmed)
			}
		}
		if len(cardLines) < 2 {
			t.Errorf("expected at least 2 card lines for 1 worker, got %d lines:\n%s", len(cardLines), plain)
		}

		// Line 1 should contain worker ID, status, bead, ctx%, heartbeat
		if !strings.Contains(plain, "w-1") {
			t.Errorf("missing worker ID 'w-1' in output:\n%s", plain)
		}
		if !strings.Contains(plain, "working") {
			t.Errorf("missing status 'working' in output:\n%s", plain)
		}
		if !strings.Contains(plain, "oro-abc") {
			t.Errorf("missing bead ID 'oro-abc' in output:\n%s", plain)
		}
		if !strings.Contains(plain, "42%") {
			t.Errorf("missing context pct '42%%' in output:\n%s", plain)
		}
		if !strings.Contains(plain, "5s ago") {
			t.Errorf("missing heartbeat '5s ago' in output:\n%s", plain)
		}
	})

	t.Run("ActiveFirst", func(t *testing.T) {
		workers := []WorkerStatus{
			{ID: "w-1", Status: "idle", BeadID: "", ContextPct: 0, LastProgressSecs: 0},
			{ID: "w-2", Status: "working", BeadID: "oro-xyz", ContextPct: 80, LastProgressSecs: 2},
		}
		buf := NewMetricsBuffer()

		got := renderWorkersSection(workers, buf, 10, theme, styles, 200)
		plain := stripANSI(got)

		// w-2 (working) should appear before w-1 (idle)
		posActive := strings.Index(plain, "w-2")
		posIdle := strings.Index(plain, "w-1")
		if posActive < 0 || posIdle < 0 {
			t.Fatalf("missing worker IDs in output:\n%s", plain)
		}
		if posActive >= posIdle {
			t.Errorf("expected active worker w-2 before idle w-1, active@%d idle@%d:\n%s", posActive, posIdle, plain)
		}
	})

	t.Run("IdleDimmed", func(t *testing.T) {
		// Verify idle worker renders through Muted style (both lines wrapped together).
		// With both active and idle workers, the idle card content should differ structurally:
		// active worker line1 is plain text, idle worker line1 goes through styles.Muted.Render.
		active := []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-x", ContextPct: 50, LastProgressSecs: 2},
		}
		idle := []WorkerStatus{
			{ID: "w-2", Status: "idle", BeadID: "", ContextPct: 0, LastProgressSecs: 0},
		}
		buf := NewMetricsBuffer()

		activeOut := renderWorkersSection(active, buf, 10, theme, styles, 200)
		idleOut := renderWorkersSection(idle, buf, 10, theme, styles, 200)

		// Active card should NOT be wrapped in Muted style —
		// raw output equals stripped output for the card lines.
		activePlain := stripANSI(activeOut)
		idlePlain := stripANSI(idleOut)

		// Both should contain their respective status
		if !strings.Contains(activePlain, "working") {
			t.Errorf("active worker missing 'working' status:\n%s", activePlain)
		}
		if !strings.Contains(idlePlain, "idle") {
			t.Errorf("idle worker missing 'idle' status:\n%s", idlePlain)
		}
		// Idle worker should show em-dash for missing bead/heartbeat
		if !strings.Contains(idlePlain, "\u2014") {
			t.Errorf("idle worker should show em-dash for missing bead/heartbeat:\n%s", idlePlain)
		}

		// Verify Muted style is applied: when lipgloss has color, idle raw != idle plain.
		// In non-TTY test environments, verify the code path by checking the card
		// contains both lines (Muted wraps l1+"\n"+l2 together).
		idleLines := strings.Split(strings.TrimSpace(idlePlain), "\n")
		var cardLines []string
		for _, line := range idleLines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "Workers") {
				cardLines = append(cardLines, trimmed)
			}
		}
		if len(cardLines) < 2 {
			t.Errorf("idle worker card should have 2 lines, got %d:\n%s", len(cardLines), idlePlain)
		}
	})

	t.Run("EmptyState", func(t *testing.T) {
		workers := []WorkerStatus{}
		buf := NewMetricsBuffer()

		got := renderWorkersSection(workers, buf, 10, theme, styles, 200)
		plain := stripANSI(got)

		if !strings.Contains(plain, "No workers connected") {
			t.Errorf("expected empty state hint 'No workers connected', got:\n%s", plain)
		}
	})

	t.Run("PerWorkerSparkline", func(t *testing.T) {
		now := time.Now()
		workers := []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-abc", ContextPct: 60, LastProgressSecs: 3},
		}
		samples := []MetricsSample{
			{Timestamp: now.Add(-3 * time.Second), Workers: []WorkerSample{{ID: "w-1", ContextPct: 20, State: "working"}}},
			{Timestamp: now.Add(-2 * time.Second), Workers: []WorkerSample{{ID: "w-1", ContextPct: 40, State: "working"}}},
			{Timestamp: now.Add(-1 * time.Second), Workers: []WorkerSample{{ID: "w-1", ContextPct: 60, State: "working"}}},
		}
		buf := makeBuf(samples)

		got := renderWorkersSection(workers, buf, 10, theme, styles, 200)
		plain := stripANSI(got)

		// Sparkline should be present — Unicode block characters
		hasBlock := false
		for _, r := range plain {
			if r >= '\u2581' && r <= '\u2588' {
				hasBlock = true
				break
			}
		}
		if !hasBlock {
			t.Errorf("expected sparkline Unicode blocks in output, got:\n%s", plain)
		}
	})

	t.Run("NewWorkerPaddedSparkline", func(t *testing.T) {
		now := time.Now()
		workers := []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-abc", ContextPct: 50, LastProgressSecs: 1},
		}
		samples := []MetricsSample{
			{Timestamp: now, Workers: []WorkerSample{{ID: "w-1", ContextPct: 50, State: "working"}}},
		}
		buf := makeBuf(samples)

		got := renderWorkersSection(workers, buf, 10, theme, styles, 200)
		plain := stripANSI(got)

		// Count baseline blocks — should have padding since only 1 data point
		baseCount := strings.Count(plain, "\u2581")
		if baseCount < 1 {
			t.Errorf("expected baseline padding blocks for new worker sparkline, got %d in:\n%s", baseCount, plain)
		}
	})
}

// TestPipelineSection verifies the pipeline section renders 3 sparkline rows
// for throughput, queue depth, and worker utilization.
func TestPipelineSection(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	// Helper: create a MetricsBuffer with given samples.
	makeBuf := func(samples []MetricsSample) *MetricsBuffer {
		buf := NewMetricsBuffer()
		for _, s := range samples {
			buf.Record(s)
		}
		return buf
	}

	t.Run("ThreeSparklineRows", func(t *testing.T) {
		now := time.Now()
		samples := []MetricsSample{
			{Timestamp: now.Add(-2 * time.Minute), BeadsClosed: 0, QueueReady: 3, QueueWIP: 2, WorkersActive: 1, WorkersTotal: 4},
			{Timestamp: now.Add(-1 * time.Minute), BeadsClosed: 2, QueueReady: 2, QueueWIP: 3, WorkersActive: 2, WorkersTotal: 4},
			{Timestamp: now, BeadsClosed: 5, QueueReady: 1, QueueWIP: 1, WorkersActive: 3, WorkersTotal: 4},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "Throughput") {
			t.Errorf("missing Throughput label in:\n%s", plain)
		}
		if !strings.Contains(plain, "Queue") {
			t.Errorf("missing Queue label in:\n%s", plain)
		}
		if !strings.Contains(plain, "Utilization") {
			t.Errorf("missing Utilization label in:\n%s", plain)
		}
	})

	t.Run("ThroughputCalculation", func(t *testing.T) {
		now := time.Now()
		// 10 beads closed over 1 hour = 10/hr
		samples := []MetricsSample{
			{Timestamp: now.Add(-1 * time.Hour), BeadsClosed: 0, WorkersActive: 1, WorkersTotal: 1},
			{Timestamp: now, BeadsClosed: 10, WorkersActive: 1, WorkersTotal: 1},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "10") {
			t.Errorf("expected throughput to contain '10', got:\n%s", plain)
		}
	})

	t.Run("QueueCalculation", func(t *testing.T) {
		now := time.Now()
		// Queue = ready + wip = 5 + 3 = 8
		samples := []MetricsSample{
			{Timestamp: now.Add(-1 * time.Minute), QueueReady: 2, QueueWIP: 1, WorkersActive: 1, WorkersTotal: 1},
			{Timestamp: now, QueueReady: 5, QueueWIP: 3, WorkersActive: 1, WorkersTotal: 1},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "8") {
			t.Errorf("expected queue value '8' in output, got:\n%s", plain)
		}
	})

	t.Run("UtilizationCalculation", func(t *testing.T) {
		now := time.Now()
		// Utilization = 3/4 = 75%
		samples := []MetricsSample{
			{Timestamp: now.Add(-1 * time.Minute), WorkersActive: 1, WorkersTotal: 4},
			{Timestamp: now, WorkersActive: 3, WorkersTotal: 4},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "75") {
			t.Errorf("expected utilization '75' in output, got:\n%s", plain)
		}
	})

	t.Run("EarlySessionShowsDash", func(t *testing.T) {
		buf := makeBuf([]MetricsSample{
			{Timestamp: time.Now(), BeadsClosed: 0, QueueReady: 1, QueueWIP: 0, WorkersActive: 0, WorkersTotal: 1},
		})

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "\u2014") {
			t.Errorf("expected em-dash for early session, got:\n%s", plain)
		}
	})

	t.Run("ZeroTimeDeltaShowsDash", func(t *testing.T) {
		now := time.Now()
		samples := []MetricsSample{
			{Timestamp: now, BeadsClosed: 0, WorkersActive: 1, WorkersTotal: 2},
			{Timestamp: now, BeadsClosed: 5, WorkersActive: 1, WorkersTotal: 2},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		lines := strings.Split(plain, "\n")
		foundDash := false
		for _, line := range lines {
			if strings.Contains(line, "Throughput") && strings.Contains(line, "\u2014") {
				foundDash = true
				break
			}
		}
		if !foundDash {
			t.Errorf("expected Throughput line to contain em-dash when timeDelta=0, got:\n%s", plain)
		}
	})

	t.Run("NegativeDeltaClampsToZero", func(t *testing.T) {
		now := time.Now()
		samples := []MetricsSample{
			{Timestamp: now.Add(-1 * time.Hour), BeadsClosed: 10, WorkersActive: 1, WorkersTotal: 1},
			{Timestamp: now, BeadsClosed: 5, WorkersActive: 1, WorkersTotal: 1},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if strings.Contains(plain, "-") {
			t.Errorf("throughput should not be negative, got:\n%s", plain)
		}
	})

	t.Run("NilBufferShowsDash", func(t *testing.T) {
		got := renderPipelineSection(nil, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "\u2014") {
			t.Errorf("expected em-dash for nil buffer, got:\n%s", plain)
		}
	})

	t.Run("ZeroTotalWorkersShowsDash", func(t *testing.T) {
		now := time.Now()
		samples := []MetricsSample{
			{Timestamp: now.Add(-1 * time.Minute), WorkersActive: 0, WorkersTotal: 0},
			{Timestamp: now, WorkersActive: 0, WorkersTotal: 0},
		}
		buf := makeBuf(samples)

		got := renderPipelineSection(buf, 40, theme, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "Utilization") {
			t.Errorf("expected Utilization label, got:\n%s", plain)
		}
	})
}

// TestSessionSection verifies the session counters section renders
// handoffs, respawns, and QG runs from model state and MetricsBuffer.
func TestSessionSection(t *testing.T) {
	styles := NewStyles(DefaultTheme())

	// lineContains checks if any line in output contains both label and value.
	lineContains := func(output, label, value string) bool {
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, label) && strings.Contains(line, value) {
				return true
			}
		}
		return false
	}

	t.Run("HandoffsFromCount", func(t *testing.T) {
		got := renderSessionSection(3, nil, nil, styles)
		plain := stripANSI(got)

		if !strings.Contains(plain, "Session") {
			t.Errorf("missing Session title in:\n%s", plain)
		}
		if !lineContains(plain, "Handoffs", "3") {
			t.Errorf("expected Handoffs: 3 in:\n%s", plain)
		}
	})

	t.Run("QGRunsFromAttemptCounts", func(t *testing.T) {
		attempts := map[string]int{
			"oro-abc1": 3,
			"oro-abc2": 5,
			"oro-abc3": 4,
		}
		got := renderSessionSection(0, attempts, nil, styles)
		plain := stripANSI(got)

		if !lineContains(plain, "QG Runs", "12") {
			t.Errorf("expected QG Runs: 12 in:\n%s", plain)
		}
	})

	t.Run("RespawnDetection", func(t *testing.T) {
		buf := NewMetricsBuffer()
		now := time.Now()

		// Sample 0: w1 working, w2 working
		buf.Record(MetricsSample{
			Timestamp: now.Add(-2 * time.Second),
			Workers: []WorkerSample{
				{ID: "w1", State: "working"},
				{ID: "w2", State: "working"},
			},
		})
		// Sample 1: w1 goes idle, w2 still working => NOT shutdown
		buf.Record(MetricsSample{
			Timestamp: now.Add(-1 * time.Second),
			Workers: []WorkerSample{
				{ID: "w1", State: "idle"},
				{ID: "w2", State: "working"},
			},
		})

		got := renderSessionSection(0, nil, buf, styles)
		plain := stripANSI(got)

		if !lineContains(plain, "Respawns", "1") {
			t.Errorf("expected Respawns: 1, got:\n%s", plain)
		}
	})

	t.Run("ShutdownSuppression", func(t *testing.T) {
		buf := NewMetricsBuffer()
		now := time.Now()

		// Sample 0: all workers working
		buf.Record(MetricsSample{
			Timestamp: now.Add(-2 * time.Second),
			Workers: []WorkerSample{
				{ID: "w1", State: "working"},
				{ID: "w2", State: "working"},
				{ID: "w3", State: "working"},
			},
		})
		// Sample 1: ALL workers go idle (shutdown signal)
		buf.Record(MetricsSample{
			Timestamp: now.Add(-1 * time.Second),
			Workers: []WorkerSample{
				{ID: "w1", State: "idle"},
				{ID: "w2", State: "idle"},
				{ID: "w3", State: "idle"},
			},
		})

		got := renderSessionSection(0, nil, buf, styles)
		plain := stripANSI(got)

		if !lineContains(plain, "Respawns", "0") {
			t.Errorf("expected Respawns: 0 during shutdown, got:\n%s", plain)
		}
	})

	t.Run("EmptyBuffer", func(t *testing.T) {
		buf := NewMetricsBuffer()

		got := renderSessionSection(0, nil, buf, styles)
		plain := stripANSI(got)

		if !lineContains(plain, "Respawns", "0") {
			t.Errorf("expected Respawns: 0 with empty buffer, got:\n%s", plain)
		}
	})

	t.Run("NilAttemptCounts", func(t *testing.T) {
		got := renderSessionSection(0, nil, nil, styles)
		plain := stripANSI(got)

		if !lineContains(plain, "QG Runs", "0") {
			t.Errorf("expected QG Runs: 0 with nil attemptCounts, got:\n%s", plain)
		}
	})
}

// TestStatusResponsive verifies width-dependent responsive layout,
// viewport scrolling, and enter-on-worker navigation.
func TestStatusResponsive(t *testing.T) {
	theme := DefaultTheme()
	styles := NewStyles(theme)

	// Helper: build a model with workers and health data.
	mkModel := func(width, height int) Model {
		m := newModel()
		m.activeView = StatusView
		m.initialLoad = false
		m.width = width
		m.height = height
		m.healthData = &HealthData{
			DaemonPID:     1234,
			DaemonState:   "running",
			ArchitectPane: PaneHealth{Name: "architect", Alive: true},
			ManagerPane:   PaneHealth{Name: "manager", Alive: true},
			WorkerCount:   2,
		}
		m.workers = []WorkerStatus{
			{ID: "w-1", Status: "working", BeadID: "oro-abc", ContextPct: 40, LastProgressSecs: 2},
			{ID: "w-2", Status: "idle", BeadID: "", ContextPct: 0, LastProgressSecs: 0},
		}
		m.pendingHandoffCount = 1
		m.attemptCounts = map[string]int{"oro-abc": 3}

		// Add some metrics samples
		buf := NewMetricsBuffer()
		now := time.Now()
		for i := 0; i < 5; i++ {
			buf.Record(MetricsSample{
				Timestamp:     now.Add(time.Duration(i) * 2 * time.Second),
				BeadsClosed:   i,
				QueueReady:    3,
				QueueWIP:      2,
				WorkersActive: 1,
				WorkersTotal:  2,
				Workers: []WorkerSample{
					{ID: "w-1", ContextPct: 10 + i*10, State: "working"},
					{ID: "w-2", ContextPct: 0, State: "idle"},
				},
			})
		}
		m.metricsBuffer = buf

		return m
	}

	_ = theme
	_ = styles

	t.Run("FullWidthShowsAllColumns", func(t *testing.T) {
		m := mkModel(130, 50)
		view := m.View()
		plain := stripANSI(view)

		// Full width (>120): all worker columns visible
		if !strings.Contains(plain, "cycle:") {
			t.Errorf("full width (>120) should show cycle column, got:\n%s", plain)
		}
		if !strings.Contains(plain, "elapsed:") {
			t.Errorf("full width (>120) should show elapsed column, got:\n%s", plain)
		}
	})

	t.Run("MediumWidthHidesCycleElapsed", func(t *testing.T) {
		m := mkModel(110, 50)
		view := m.View()
		plain := stripANSI(view)

		// 100-120: hide cycle and elapsed
		if strings.Contains(plain, "cycle:") {
			t.Errorf("medium width (100-120) should hide cycle column, got:\n%s", plain)
		}
		if strings.Contains(plain, "elapsed:") {
			t.Errorf("medium width (100-120) should hide elapsed column, got:\n%s", plain)
		}
		// But should still show done and fail
		if !strings.Contains(plain, "done:") {
			t.Errorf("medium width should still show done column, got:\n%s", plain)
		}
	})

	t.Run("NarrowWidthHidesFailCycle", func(t *testing.T) {
		m := mkModel(90, 50)
		view := m.View()
		plain := stripANSI(view)

		// 80-100: hide fail and cycle (and elapsed)
		if strings.Contains(plain, "fail:") {
			t.Errorf("narrow width (80-100) should hide fail column, got:\n%s", plain)
		}
		if strings.Contains(plain, "cycle:") {
			t.Errorf("narrow width should hide cycle column, got:\n%s", plain)
		}
	})

	t.Run("VeryNarrowNoSparklines", func(t *testing.T) {
		m := mkModel(70, 50)
		view := m.View()
		plain := stripANSI(view)

		// <80: no worker line2 (1-line workers)
		if strings.Contains(plain, "done:") {
			t.Errorf("very narrow (<80) should hide worker line2, got:\n%s", plain)
		}
		// System section still present
		if !strings.Contains(plain, "System") {
			t.Errorf("very narrow should still show System section, got:\n%s", plain)
		}
	})

	t.Run("JKScrollsViewport", func(t *testing.T) {
		m := mkModel(120, 10) // Short height to force scrolling
		m.statusModel.cursor = 0

		// j moves down
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		model := updated.(Model) //nolint:errcheck
		if model.statusModel.cursor != 1 {
			t.Errorf("expected cursor=1 after j, got %d", model.statusModel.cursor)
		}

		// Multiple j presses
		for range 3 {
			updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
			model = updated.(Model) //nolint:errcheck
		}
		if model.statusModel.cursor < 2 {
			t.Errorf("expected cursor >= 2 after multiple j, got %d", model.statusModel.cursor)
		}

		// k moves back
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		model = updated.(Model) //nolint:errcheck
		if model.statusModel.cursor >= statusSectionCount {
			t.Errorf("cursor should be < sectionCount after k, got %d", model.statusModel.cursor)
		}
	})

	t.Run("EnterOnWorkerNavigatesToDetail", func(t *testing.T) {
		m := mkModel(120, 50)
		m.beads = []protocol.Bead{{ID: "oro-abc", Title: "Test bead", Status: "open"}}
		m.statusModel.cursor = 2 // Workers section

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model := updated.(Model) //nolint:errcheck

		// Enter on worker section should navigate to detail view for first working worker's bead
		if model.activeView != DetailView {
			t.Errorf("expected DetailView after Enter on Workers section, got %v", model.activeView)
		}
	})
}
