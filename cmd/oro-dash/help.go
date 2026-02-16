package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// helpBinding represents a key binding with its description.
type helpBinding struct {
	key  string
	desc string
}

// getBoardHelpBindings returns help bindings for BoardView.
func getBoardHelpBindings() []helpBinding {
	return []helpBinding{
		{"j/k or ↑/↓", "Navigate beads"},
		{"h/l or ←/→", "Move between columns"},
		{"tab/shift+tab", "Move between columns"},
		{"enter", "View bead details"},
		{"i", "Show insights view"},
		{"/", "Open search"},
		{"?", "Toggle help"},
		{"q or ctrl+c", "Quit"},
	}
}

// getDetailHelpBindings returns help bindings for DetailView.
func getDetailHelpBindings() []helpBinding {
	return []helpBinding{
		{"tab/shift+tab", "Switch between tabs"},
		{"esc or backspace", "Return to board"},
		{"?", "Toggle help"},
		{"q or ctrl+c", "Quit"},
	}
}

// getInsightsHelpBindings returns help bindings for InsightsView.
func getInsightsHelpBindings() []helpBinding {
	return []helpBinding{
		{"esc", "Return to board"},
		{"?", "Toggle help"},
		{"q or ctrl+c", "Quit"},
	}
}

// getSearchHelpBindings returns help bindings for SearchView.
func getSearchHelpBindings() []helpBinding {
	return []helpBinding{
		{"↑↓ or j/k", "Navigate results"},
		{"enter", "View selected bead"},
		{"esc", "Cancel search"},
		{"backspace", "Delete character"},
		{"Type to search", "Use p:N, s:STATUS, t:TYPE filters"},
	}
}

// getHelpBindingsForView returns help bindings for the given view.
func getHelpBindingsForView(view ViewType) []helpBinding {
	switch view {
	case BoardView:
		return getBoardHelpBindings()
	case DetailView:
		return getDetailHelpBindings()
	case InsightsView:
		return getInsightsHelpBindings()
	case SearchView:
		return getSearchHelpBindings()
	default:
		return getBoardHelpBindings()
	}
}

// getViewName returns the display name for a view.
func getViewName(view ViewType) string {
	switch view {
	case BoardView:
		return "Board View"
	case DetailView:
		return "Detail View"
	case InsightsView:
		return "Insights View"
	case SearchView:
		return "Search"
	default:
		return "Unknown View"
	}
}

// renderHelpOverlay renders the help overlay panel.
func (m Model) renderHelpOverlay() string {
	title := m.renderHelpTitle()
	content := m.renderHelpContent()
	footer := m.renderHelpFooter()

	return lipgloss.JoinVertical(lipgloss.Left, title, content, footer)
}

// renderHelpTitle renders the help overlay title.
func (m Model) renderHelpTitle() string {
	viewName := getViewName(m.previousView)
	return m.styles.HelpTitle.Render("Help - " + viewName)
}

// renderHelpContent renders the key bindings list.
func (m Model) renderHelpContent() string {
	bindings := getHelpBindingsForView(m.previousView)

	var contentBuilder strings.Builder
	keyStyle := m.styles.HelpKey.Width(20)

	for _, binding := range bindings {
		key := keyStyle.Render(binding.key)
		desc := m.styles.HelpDesc.Render(binding.desc)
		contentBuilder.WriteString(lipgloss.JoinHorizontal(lipgloss.Left, key, desc))
		contentBuilder.WriteString("\n")
	}

	return m.styles.HelpContent.Render(contentBuilder.String())
}

// renderHelpFooter renders the help overlay footer with dismissal instructions.
func (m Model) renderHelpFooter() string {
	return m.styles.HelpFooter.Render("Press ? or Esc to close")
}
