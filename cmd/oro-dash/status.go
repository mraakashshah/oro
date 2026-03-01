package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"oro/pkg/protocol"
)

// StatusModel holds the state for the StatusView.
type StatusModel struct {
	cursor int // Index of the currently selected section
}

// statusSectionCount is the number of navigable sections in the status view.
const statusSectionCount = 5 // System, Panes, Workers, Pipeline, Session

// View renders the StatusView showing system sections.
// When healthData is nil, shows "Connecting..." placeholder.
func (s StatusModel) View(
	theme Theme, styles Styles, healthData *HealthData,
	workers []WorkerStatus, pendingHandoffs int, attemptCounts map[string]int,
	buf *MetricsBuffer, width, height int,
) string {
	if healthData == nil {
		offlineInfo := lipgloss.JoinVertical(lipgloss.Left,
			styles.SectionTitle.Render("System"),
			styles.Muted.Render("Daemon: offline"),
			styles.Muted.Render(fmt.Sprintf("Socket: %s", defaultSocketPath())),
			styles.Muted.Render("Try: oro start"),
			"",
			renderWorkersSection(workers, buf, width/3, theme, styles, width),
		)
		return lipgloss.NewStyle().Width(width).Height(height).Render(offlineInfo)
	}

	sparkW := width / 3
	rawSections := []string{
		renderSystemSection(healthData, styles),
		renderPanesStatusSection(healthData, styles),
		renderWorkersSection(workers, buf, sparkW, theme, styles, width),
		renderPipelineSection(buf, width, theme, styles),
		renderSessionSection(pendingHandoffs, attemptCounts, buf, styles),
	}

	// Apply cursor highlight to the active section.
	sections := make([]string, len(rawSections))
	for i, sec := range rawSections {
		if i == s.cursor {
			sections[i] = "▸ " + sec
		} else {
			sections[i] = "  " + sec
		}
	}

	return lipgloss.NewStyle().Width(width).Height(height).
		Render(lipgloss.JoinVertical(lipgloss.Left, sections...))
}

// renderSystemSection renders the System section with daemon status and uptime.
func renderSystemSection(hd *HealthData, styles Styles) string {
	title := styles.SectionTitle.Render("System")
	stateLine := fmt.Sprintf("Daemon: %s (PID %d)", hd.DaemonState, hd.DaemonPID)
	socketLine := fmt.Sprintf("Socket: %s", defaultSocketPath())

	return lipgloss.JoinVertical(lipgloss.Left, title, stateLine, socketLine)
}

// renderPanesStatusSection renders the Panes section with architect and manager status.
func renderPanesStatusSection(hd *HealthData, styles Styles) string {
	title := styles.SectionTitle.Render("Panes")

	var sb strings.Builder
	sb.WriteString(renderPaneStatusLine(hd.ArchitectPane))
	sb.WriteString("\n")
	sb.WriteString(renderPaneStatusLine(hd.ManagerPane))

	return lipgloss.JoinVertical(lipgloss.Left, title, sb.String())
}

// renderPaneStatusLine renders a single pane's status as a line.
func renderPaneStatusLine(pane PaneHealth) string {
	status := "offline"
	if pane.Alive {
		status = "alive"
	}
	line := fmt.Sprintf("%s: %s", pane.Name, status)
	if pane.LastActivity != "" {
		line += fmt.Sprintf(" (last: %s)", pane.LastActivity)
	}
	return line
}

// renderWorkersSection renders detailed 2-line worker cards with per-worker sparklines.
// Line 1: [id] [status] bead:[beadID] ctx:[pct]% hb:[heartbeat]
// Line 2: [sparkline] done:— fail:— cycle:— elapsed:— (responsive to viewWidth)
// Active workers appear first; idle workers are dimmed. Empty state shows a hint.
func renderWorkersSection(
	workers []WorkerStatus, buf *MetricsBuffer, sparkWidth int, theme Theme, styles Styles, viewWidth int,
) string {
	title := styles.SectionTitle.Render("Workers")

	if len(workers) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, title,
			styles.Muted.Render("No workers connected"))
	}

	sorted := sortWorkersByActivity(workers)
	history := extractWorkerHistory(buf, sparkWidth)

	var sb strings.Builder
	for i, w := range sorted {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(renderWorkerCard(w, history[w.ID], sparkWidth, theme, styles, viewWidth))
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, sb.String())
}

// sortWorkersByActivity returns workers with active ("working") first, then idle.
func sortWorkersByActivity(workers []WorkerStatus) []WorkerStatus {
	sorted := make([]WorkerStatus, len(workers))
	copy(sorted, workers)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Status == "working" && sorted[j].Status != "working"
	})
	return sorted
}

// extractWorkerHistory builds per-worker context% history from the MetricsBuffer.
// Returns a map of worker ID to float64 values (oldest first).
func extractWorkerHistory(buf *MetricsBuffer, width int) map[string][]float64 {
	result := make(map[string][]float64)
	if buf == nil {
		return result
	}
	for _, s := range buf.Last(width) {
		for _, ws := range s.Workers {
			result[ws.ID] = append(result[ws.ID], float64(ws.ContextPct))
		}
	}
	return result
}

// renderWorkerCard renders a single worker card. At viewWidth >= 80, renders 2 lines
// (status + sparkline/stats). Below 80, renders only line 1 for compact display.
func renderWorkerCard(
	w WorkerStatus, history []float64, sparkWidth int, theme Theme, styles Styles, viewWidth int,
) string {
	l1 := formatWorkerLine1(w)

	if viewWidth < 60 {
		if w.Status == "idle" {
			return styles.Muted.Render(l1)
		}
		return l1
	}

	l2 := formatWorkerLine2(history, sparkWidth, theme, styles, viewWidth)

	if w.Status == "idle" {
		return styles.Muted.Render(l1 + "\n" + l2)
	}
	return l1 + "\n" + l2
}

// formatWorkerLine1 formats the first line: id status bead ctx hb.
func formatWorkerLine1(w WorkerStatus) string {
	const emDash = "\u2014"

	bead := w.BeadID
	if bead == "" {
		bead = emDash
	}

	hb := emDash
	if w.LastProgressSecs > 0 {
		hb = fmt.Sprintf("%.0fs ago", w.LastProgressSecs)
	}

	return fmt.Sprintf("%s %s bead:%s ctx:%d%% hb:%s",
		w.ID, w.Status, bead, w.ContextPct, hb)
}

// formatWorkerLine2 formats the second line: sparkline + responsive stat columns.
// >120: all columns (done/fail/cycle/elapsed). 100-120: hide cycle+elapsed.
// 80-100: hide fail+cycle+elapsed. <80: caller skips line2 entirely.
func formatWorkerLine2(
	history []float64, sparkWidth int, theme Theme, styles Styles, viewWidth int,
) string {
	const emDash = "\u2014"

	spark := renderSparkline(history, sparkWidth, theme.Primary, styles)
	if spark == "" {
		spark = renderSparkline([]float64{0}, sparkWidth, theme.Primary, styles)
	}

	switch {
	case viewWidth > 120:
		return fmt.Sprintf("%s done:%s fail:%s cycle:%s elapsed:%s",
			spark, emDash, emDash, emDash, emDash)
	case viewWidth > 100:
		return fmt.Sprintf("%s done:%s fail:%s",
			spark, emDash, emDash)
	default:
		return fmt.Sprintf("%s done:%s", spark, emDash)
	}
}

// renderPipelineSection renders throughput, queue, and utilization sparkline rows.
// When the buffer has fewer than 2 samples or is nil, shows em-dash placeholders.
func renderPipelineSection(buf *MetricsBuffer, width int, theme Theme, styles Styles) string {
	const emDash = "\u2014"

	samples := pipelineSamples(buf, width)
	title := styles.SectionTitle.Render("Pipeline")
	tpLine := renderThroughputLine(samples, width, theme, styles, emDash)
	qLine := renderQueueLine(samples, width, theme, styles, emDash)
	uLine := renderUtilizationLine(samples, width, theme, styles, emDash)

	return lipgloss.JoinVertical(lipgloss.Left, title, tpLine, qLine, uLine)
}

// pipelineSamples returns the samples slice from buf, or nil when unavailable.
func pipelineSamples(buf *MetricsBuffer, width int) []MetricsSample {
	if buf == nil {
		return nil
	}
	return buf.Last(width)
}

// renderThroughputLine renders the throughput sparkline row.
func renderThroughputLine(
	samples []MetricsSample, width int, theme Theme, styles Styles, emDash string,
) string {
	if len(samples) < 2 {
		return fmt.Sprintf("  Throughput: %s", emDash)
	}

	earliest, latest := samples[0], samples[len(samples)-1]
	deltaSec := latest.Timestamp.Sub(earliest.Timestamp).Seconds()

	if deltaSec == 0 {
		return fmt.Sprintf("  Throughput: %s", emDash)
	}

	delta := latest.BeadsClosed - earliest.BeadsClosed
	if delta < 0 {
		delta = 0
	}

	rate := float64(delta) / deltaSec * 3600
	values := throughputValues(samples)
	spark := renderSparkline(values, width, theme.Success, styles)

	return fmt.Sprintf("  Throughput: %.0f/hr  %s", rate, spark)
}

// throughputValues computes per-adjacent-pair throughput in beads/hr.
func throughputValues(samples []MetricsSample) []float64 {
	if len(samples) < 2 {
		return nil
	}
	vals := make([]float64, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		dt := samples[i].Timestamp.Sub(samples[i-1].Timestamp).Seconds()
		d := samples[i].BeadsClosed - samples[i-1].BeadsClosed
		if d < 0 {
			d = 0
		}
		if dt > 0 {
			vals[i-1] = float64(d) / dt * 3600
		}
	}
	return vals
}

// renderQueueLine renders the queue depth sparkline row.
func renderQueueLine(
	samples []MetricsSample, width int, theme Theme, styles Styles, emDash string,
) string {
	if len(samples) < 2 {
		return fmt.Sprintf("  Queue: %s", emDash)
	}

	latest := samples[len(samples)-1]
	depth := latest.QueueReady + latest.QueueWIP

	values := make([]float64, len(samples))
	for i, s := range samples {
		values[i] = float64(s.QueueReady + s.QueueWIP)
	}

	spark := renderSparkline(values, width, theme.Warning, styles)
	return fmt.Sprintf("  Queue: %d  %s", depth, spark)
}

// renderUtilizationLine renders the worker utilization sparkline row.
func renderUtilizationLine(
	samples []MetricsSample, width int, theme Theme, styles Styles, emDash string,
) string {
	if len(samples) < 2 {
		return fmt.Sprintf("  Utilization: %s", emDash)
	}

	latest := samples[len(samples)-1]
	pct := utilizationPct(latest.WorkersActive, latest.WorkersTotal)

	values := make([]float64, len(samples))
	for i, s := range samples {
		values[i] = utilizationPct(s.WorkersActive, s.WorkersTotal)
	}

	spark := renderSparkline(values, width, theme.Primary, styles)
	return fmt.Sprintf("  Utilization: %.0f%%  %s", pct, spark)
}

// utilizationPct returns active/total*100, safe for zero total.
func utilizationPct(active, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(active) / float64(total) * 100
}

// renderSessionSection renders the Session section with handoff, respawn, and QG run counters.
func renderSessionSection(pendingHandoffs int, attemptCounts map[string]int, buf *MetricsBuffer, styles Styles) string {
	title := styles.SectionTitle.Render("Session")

	respawns := countRespawns(buf)

	qgRuns := 0
	for _, v := range attemptCounts {
		qgRuns += v
	}

	body := fmt.Sprintf("  Handoffs: %d\n  Respawns: %d\n  QG Runs:  %d", pendingHandoffs, respawns, qgRuns)

	return lipgloss.JoinVertical(lipgloss.Left, title, body)
}

// countRespawns counts working->idle transitions in the MetricsBuffer history,
// excluding samples where ALL workers go idle simultaneously (daemon shutdown).
func countRespawns(buf *MetricsBuffer) int {
	if buf == nil {
		return 0
	}
	samples := buf.Last(buf.Len())
	if len(samples) < 2 {
		return 0
	}

	count := 0
	for i := 0; i < len(samples)-1; i++ {
		count += countPairRespawns(samples[i], samples[i+1])
	}
	return count
}

// countPairRespawns counts working->idle transitions between two adjacent samples.
// Returns 0 if all workers in next are idle (shutdown suppression).
func countPairRespawns(prev, next MetricsSample) int {
	if allWorkersIdle(next.Workers) {
		return 0
	}

	nextStates := make(map[string]string, len(next.Workers))
	for _, w := range next.Workers {
		nextStates[w.ID] = w.State
	}

	count := 0
	for _, w := range prev.Workers {
		if w.State == "working" {
			if ns, ok := nextStates[w.ID]; ok && ns == "idle" {
				count++
			}
		}
	}
	return count
}

// allWorkersIdle returns true if all workers in the slice are in "idle" state.
// Returns false for an empty slice.
func allWorkersIdle(workers []WorkerSample) bool {
	if len(workers) == 0 {
		return false
	}
	for _, w := range workers {
		if w.State != "idle" {
			return false
		}
	}
	return true
}

// statusSectionWorkers is the index of the Workers section in the status view.
const statusSectionWorkers = 2

// handleStatusViewKeys processes keyboard input in StatusView.
func (m Model) handleStatusViewKeys(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.activeView = m.previousNavView
	case "j", "down":
		if m.statusModel.cursor < statusSectionCount-1 {
			m.statusModel.cursor++
		}
	case "k", "up":
		if m.statusModel.cursor > 0 {
			m.statusModel.cursor--
		}
	case "enter":
		if m.statusModel.cursor == statusSectionWorkers {
			return m.navigateToWorkerBead()
		}
	case "H", "w":
		// Already in StatusView, no-op
	case "L":
		m.activeView = ListView
	}
	return m, nil
}

// navigateToWorkerBead finds the first working worker's bead and opens DetailView.
func (m Model) navigateToWorkerBead() (tea.Model, tea.Cmd) {
	for _, w := range m.workers {
		if w.Status != "working" || w.BeadID == "" {
			continue
		}
		for _, b := range m.beads {
			if b.ID != w.BeadID {
				continue
			}
			bd := protocol.BeadDetail{
				ID:                 b.ID,
				Title:              b.Title,
				Status:             b.Status,
				AcceptanceCriteria: b.AcceptanceCriteria,
				Dependencies:       b.Dependencies,
			}
			dm := newDetailModel(bd, m.theme, m.styles)
			m.detailModel = &dm
			m.activeView = DetailView
			return m, nil
		}
	}
	return m, nil
}
