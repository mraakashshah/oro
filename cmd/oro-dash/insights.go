package main

import (
	"fmt"
	"strings"
)

// InsightsModel holds the insights view state.
type InsightsModel struct {
	graph *DependencyGraph
}

// NewInsightsModel creates a new insights model from beads.
func NewInsightsModel(beads []BeadWithDeps) *InsightsModel {
	return &InsightsModel{
		graph: NewDependencyGraph(beads),
	}
}

// Render renders the insights view with critical path, bottlenecks, cycles, and triage flags.
func (m *InsightsModel) Render(styles Styles) string {
	if len(m.graph.beads) == 0 {
		return renderEmptyState(styles)
	}

	sections := []string{
		renderCriticalPath(m.graph, styles),
		renderBottlenecks(m.graph, styles),
		renderCycles(m.graph, styles),
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

func renderCriticalPath(graph *DependencyGraph, styles Styles) string {
	path, err := graph.CriticalPath()

	var content string
	switch {
	case err != nil:
		content = styles.InsightsError.Render("  Error: " + err.Error())
	case len(path) == 0:
		content = styles.InsightsNoData.Render("  No data")
	default:
		// Format as chain: bead-3 → bead-2 → bead-1
		content = "  " + strings.Join(path, " → ")
	}

	return renderSectionTitle("Critical Path", styles) + "\n" + content
}

func renderBottlenecks(graph *DependencyGraph, styles Styles) string {
	bottlenecks := graph.Bottlenecks()

	var content string
	if len(bottlenecks) == 0 {
		content = styles.InsightsNoData.Render("  No data")
	} else {
		rows := make([]string, 0, len(bottlenecks))
		for _, b := range bottlenecks {
			row := fmt.Sprintf("  %s: %d", b.BeadID, b.BlockedCount)
			rows = append(rows, row)
		}
		content = strings.Join(rows, "\n")
	}

	return renderSectionTitle("Bottlenecks", styles) + "\n" + content
}

func renderCycles(graph *DependencyGraph, styles Styles) string {
	_, err := graph.CriticalPath()

	var content string
	if err != nil {
		content = styles.InsightsError.Render("  Detected")
	} else {
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
			// severityColor is dynamic (per-finding) — pick from pre-computed styles per severity.
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
