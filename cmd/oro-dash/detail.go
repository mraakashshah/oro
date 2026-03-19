// Package main implements the oro-dash TUI dashboard.
package main

import (
	"strings"

	"oro/pkg/protocol"

	"github.com/charmbracelet/lipgloss"
)

// Theme represents the color theme for the dashboard.
type Theme struct {
	// Add theme colors as needed
}

// DefaultTheme returns the default theme.
func DefaultTheme() Theme {
	return Theme{}
}

// Styles represents the computed lipgloss styles for the dashboard.
type Styles struct {
	DetailTitle lipgloss.Style
	DetailBold  lipgloss.Style
}

// NewStyles creates a new Styles struct with the given theme.
func NewStyles(theme Theme) Styles {
	return Styles{
		DetailTitle: lipgloss.NewStyle().Bold(true),
		DetailBold:  lipgloss.NewStyle().Bold(true),
	}
}

// DetailModel represents the detail drilldown view for a single bead.
type DetailModel struct {
	bead   protocol.BeadDetail
	theme  Theme
	styles Styles
}

// newDetailModel creates a new DetailModel for the given bead.
func newDetailModel(bead protocol.BeadDetail, theme Theme, styles Styles) DetailModel {
	return DetailModel{
		bead:   bead,
		theme:  theme,
		styles: styles,
	}
}

// renderOverviewTab renders the Overview tab with bead details.
func (d DetailModel) renderOverviewTab(styles Styles) string {
	var lines []string

	// Title and ID
	lines = append(lines,
		styles.DetailTitle.Render("Title: ")+d.bead.Title,
		"ID: "+d.bead.ID,
	)

	// Owner (if specified)
	if d.bead.Owner != "" {
		lines = append(lines, "Owner: "+d.bead.Owner)
	}

	// Model (if specified)
	if d.bead.Model != "" {
		lines = append(lines, "Model: "+d.bead.Model)
	}

	// Description (if specified)
	if d.bead.Description != "" {
		lines = append(lines,
			"",
			styles.DetailBold.Render("Description:"),
			d.bead.Description,
		)
	}

	return strings.Join(lines, "\n")
}

func main() {
	// oro-dash TUI dashboard implementation
}
