package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
func (m *InsightsModel) Render() string {
	theme := DefaultTheme()

	if len(m.graph.beads) == 0 {
		return renderEmptyState(theme)
	}

	sections := []string{
		renderCriticalPath(m.graph, theme),
		renderBottlenecks(m.graph, theme),
		renderCycles(m.graph, theme),
		renderTriageFlags(m.graph, theme),
	}

	return strings.Join(sections, "\n\n")
}

func renderEmptyState(theme Theme) string {
	dimStyle := lipgloss.NewStyle().Foreground(theme.Muted)

	return strings.Join([]string{
		renderSectionTitle("Critical Path", theme),
		dimStyle.Render("  No data"),
		"",
		renderSectionTitle("Bottlenecks", theme),
		dimStyle.Render("  No data"),
		"",
		renderSectionTitle("Cycles", theme),
		dimStyle.Render("  No data"),
		"",
		renderSectionTitle("Triage Flags", theme),
		dimStyle.Render("  No data"),
	}, "\n")
}

func renderSectionTitle(title string, theme Theme) string {
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary)
	return style.Render(title)
}

func renderCriticalPath(graph *DependencyGraph, theme Theme) string {
	path, err := graph.CriticalPath()

	var content string
	switch {
	case err != nil:
		content = lipgloss.NewStyle().Foreground(theme.Error).Render("  Error: " + err.Error())
	case len(path) == 0:
		content = lipgloss.NewStyle().Foreground(theme.Muted).Render("  No data")
	default:
		// Format as chain: bead-3 → bead-2 → bead-1
		content = "  " + strings.Join(path, " → ")
	}

	return renderSectionTitle("Critical Path", theme) + "\n" + content
}

func renderBottlenecks(graph *DependencyGraph, theme Theme) string {
	bottlenecks := graph.Bottlenecks()

	var content string
	if len(bottlenecks) == 0 {
		content = lipgloss.NewStyle().Foreground(theme.Muted).Render("  No data")
	} else {
		rows := make([]string, 0, len(bottlenecks))
		for _, b := range bottlenecks {
			row := fmt.Sprintf("  %s: %d", b.BeadID, b.BlockedCount)
			rows = append(rows, row)
		}
		content = strings.Join(rows, "\n")
	}

	return renderSectionTitle("Bottlenecks", theme) + "\n" + content
}

func renderCycles(graph *DependencyGraph, theme Theme) string {
	_, err := graph.CriticalPath()

	var content string
	if err != nil {
		content = lipgloss.NewStyle().Foreground(theme.Error).Render("  Detected")
	} else {
		content = lipgloss.NewStyle().Foreground(theme.Success).Render("  None")
	}

	return renderSectionTitle("Cycles", theme) + "\n" + content
}

func renderTriageFlags(graph *DependencyGraph, theme Theme) string {
	flags := graph.TriageFlags()

	var content string
	if len(flags) == 0 {
		content = lipgloss.NewStyle().Foreground(theme.Muted).Render("  No data")
	} else {
		rows := make([]string, 0, len(flags))
		for _, f := range flags {
			severityColor := theme.Warning
			if f.Severity == "high" {
				severityColor = theme.Error
			}
			row := fmt.Sprintf("  %s: %s",
				lipgloss.NewStyle().Foreground(severityColor).Render(f.BeadID),
				f.Reason)
			rows = append(rows, row)
		}
		content = strings.Join(rows, "\n")
	}

	return renderSectionTitle("Triage Flags", theme) + "\n" + content
}
