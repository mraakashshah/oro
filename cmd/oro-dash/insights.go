package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// phase2Timeout is the maximum time allowed for background graph analysis.
const phase2Timeout = 500 * time.Millisecond

// Phase1Results holds the data computed immediately on data load.
type Phase1Results struct {
	// InDegree maps each beadID to the number of beads that depend on it.
	InDegree map[string]int
	// TopoOrder is the topological order of beads (prerequisites first).
	// Nil if a cycle was detected during Phase 1.
	TopoOrder []string
	// TopoErr holds any error from topological sort (e.g. cycle).
	TopoErr error
}

// Phase2Results holds the data computed asynchronously in the background.
type Phase2Results struct {
	// CriticalPath is the longest dependency chain (prerequisite first).
	CriticalPath []string
	// CriticalPathErr is non-nil when critical path computation failed (e.g. cycle).
	CriticalPathErr error
	// Bottlenecks lists beads that block the most other beads.
	Bottlenecks []Bottleneck
	// HasCycle is true when the graph contains at least one circular dependency.
	HasCycle bool
	// TimedOut is true when phase 2 did not complete within the timeout.
	TimedOut bool
}

// InsightsModel holds the insights view state with two-phase async analysis.
type InsightsModel struct {
	graph *DependencyGraph

	// phase1 is computed synchronously in NewInsightsModel.
	phase1 *Phase1Results

	// phase2 is swapped in atomically by the background goroutine.
	// Readers call Phase2() which loads it atomically.
	phase2 atomic.Pointer[Phase2Results]
}

// NewInsightsModel creates a new insights model from beads.
//
// Phase 1 (degree + topo sort) runs synchronously before returning.
// Phase 2 (critical path, bottlenecks, cycle detection) is launched in a
// background goroutine bounded by phase2Timeout; the result is swapped in
// atomically when it completes.
func NewInsightsModel(beads []BeadWithDeps) *InsightsModel {
	graph := NewDependencyGraph(beads)

	// ── Phase 1: immediate ──────────────────────────────────────────────────
	inDeg := graph.InDegrees()
	topoOrder, topoErr := graph.TopologicalOrder()

	p1 := &Phase1Results{
		InDegree:  inDeg,
		TopoOrder: topoOrder,
		TopoErr:   topoErr,
	}

	m := &InsightsModel{
		graph:  graph,
		phase1: p1,
	}

	// ── Phase 2: background with timeout ───────────────────────────────────
	go func() {
		type result struct {
			r *Phase2Results
		}
		ch := make(chan result, 1)

		go func() {
			critPath, critErr := graph.CriticalPath()
			bottlenecks := graph.Bottlenecks()
			hasCycle := critErr != nil

			ch <- result{r: &Phase2Results{
				CriticalPath:    critPath,
				CriticalPathErr: critErr,
				Bottlenecks:     bottlenecks,
				HasCycle:        hasCycle,
			}}
		}()

		select {
		case res := <-ch:
			m.phase2.Store(res.r)
		case <-time.After(phase2Timeout):
			m.phase2.Store(&Phase2Results{TimedOut: true})
		}
	}()

	return m
}

// Phase1 returns the immediately-computed phase 1 results (never nil after NewInsightsModel).
func (m *InsightsModel) Phase1() *Phase1Results {
	return m.phase1
}

// Phase2 returns the async phase 2 results, or nil if not yet available.
func (m *InsightsModel) Phase2() *Phase2Results {
	return m.phase2.Load()
}

// Render renders the insights view with critical path, bottlenecks, cycles, and triage flags.
func (m *InsightsModel) Render(styles Styles) string {
	if len(m.graph.beads) == 0 {
		return renderEmptyState(styles)
	}

	p2 := m.Phase2()

	sections := []string{
		renderCriticalPathFromResults(p2, styles),
		renderBottlenecksFromResults(p2, styles),
		renderCyclesFromResults(p2, styles),
		renderTriageFlags(m.graph, styles),
	}

	return strings.Join(sections, "\n\n")
}

func renderEmptyState(styles Styles) string {
	return strings.Join([]string{
		renderSectionTitle("Critical Path", styles),
		styles.InsightsDim.Render("  No data"),
		"",
		renderSectionTitle("Bottlenecks", styles),
		styles.InsightsDim.Render("  No data"),
		"",
		renderSectionTitle("Cycles", styles),
		styles.InsightsDim.Render("  No data"),
		"",
		renderSectionTitle("Triage Flags", styles),
		styles.InsightsDim.Render("  No data"),
	}, "\n")
}

func renderSectionTitle(title string, styles Styles) string {
	return styles.InsightsSectionTitle.Render(title)
}

// renderCriticalPathFromResults renders the Critical Path section using phase 2 results.
// Shows a loading indicator when phase 2 is not yet available.
func renderCriticalPathFromResults(p2 *Phase2Results, styles Styles) string {
	var content string
	switch {
	case p2 == nil:
		content = styles.InsightsDim.Render("  Computing…")
	case p2.TimedOut:
		content = styles.InsightsDim.Render("  Timed out")
	case p2.CriticalPathErr != nil:
		content = styles.InsightsError.Render("  Error: " + p2.CriticalPathErr.Error())
	case len(p2.CriticalPath) == 0:
		content = styles.InsightsNoData.Render("  No data")
	default:
		content = "  " + strings.Join(p2.CriticalPath, " → ")
	}
	return renderSectionTitle("Critical Path", styles) + "\n" + content
}

// renderBottlenecksFromResults renders the Bottlenecks section using phase 2 results.
func renderBottlenecksFromResults(p2 *Phase2Results, styles Styles) string {
	var content string
	switch {
	case p2 == nil:
		content = styles.InsightsDim.Render("  Computing…")
	case p2.TimedOut:
		content = styles.InsightsDim.Render("  Timed out")
	case len(p2.Bottlenecks) == 0:
		content = styles.InsightsNoData.Render("  No data")
	default:
		rows := make([]string, 0, len(p2.Bottlenecks))
		for _, b := range p2.Bottlenecks {
			row := fmt.Sprintf("  %s: %d", b.BeadID, b.BlockedCount)
			rows = append(rows, row)
		}
		content = strings.Join(rows, "\n")
	}
	return renderSectionTitle("Bottlenecks", styles) + "\n" + content
}

// renderCyclesFromResults renders the Cycles section using phase 2 results.
func renderCyclesFromResults(p2 *Phase2Results, styles Styles) string {
	var content string
	switch {
	case p2 == nil:
		content = styles.InsightsDim.Render("  Computing…")
	case p2.TimedOut:
		content = styles.InsightsDim.Render("  Timed out")
	case p2.HasCycle:
		content = styles.InsightsError.Render("  Detected")
	default:
		content = styles.InsightsSuccess.Render("  None")
	}
	return renderSectionTitle("Cycles", styles) + "\n" + content
}

func renderTriageFlags(graph *DependencyGraph, styles Styles) string {
	flags := graph.TriageFlags()
	var content string
	if len(flags) == 0 {
		content = styles.InsightsNoData.Render("  No data")
	} else {
		rows := make([]string, 0, len(flags))
		for _, f := range flags {
			severityStyle := styles.Muted
			if f.Severity == "high" {
				severityStyle = styles.Error
			}
			row := fmt.Sprintf("  %s: %s",
				severityStyle.Render(f.BeadID),
				f.Reason)
			rows = append(rows, row)
		}
		content = strings.Join(rows, "\n")
	}
	return renderSectionTitle("Triage Flags", styles) + "\n" + content
}
