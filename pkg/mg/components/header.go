package components

import (
	"fmt"
	"strings"

	"oro/pkg/mg/data"
	"oro/pkg/mg"

	"charm.land/lipgloss/v2"
)

// Header renders the top title bar with bead string and counts.
type Header struct {
	Width          int
	Groups         map[data.ParadeStatus][]data.Issue
	WorkerCount    int
	BeadOffset     int    // shimmer animation offset, incremented by tick
	CurrentIssueID string // active issue shown in the header
}

// View renders the header.
func (h Header) View() string {
	rolling := len(h.Groups[data.ParadeRolling])
	linedUp := len(h.Groups[data.ParadeLinedUp])
	stalled := len(h.Groups[data.ParadeStalled])
	total := rolling + linedUp + stalled + len(h.Groups[data.ParadePastTheStand])

	titleStr := fmt.Sprintf("%s MARDI GRAS %s", mg.FleurDeLis, mg.FleurDeLis)
	title := mg.HeaderStyle.Render(mg.ApplyMardiGrasGradient(titleStr))

	counts := mg.HeaderCounts.Render(fmt.Sprintf(
		" %d ⊘  %d ♪  %d ●  %d ✓ ",
		stalled, linedUp, rolling, len(h.Groups[data.ParadePastTheStand]),
	))

	workerInfo := ""
	if h.WorkerCount > 0 {
		workerStyle := lipgloss.NewStyle().Foreground(mg.StatusAgent).Bold(true)
		workerInfo = workerStyle.Render(fmt.Sprintf(" %s%d", mg.SymWorker, h.WorkerCount))
	}

	currentInfo := ""
	if h.CurrentIssueID != "" {
		currentStyle := lipgloss.NewStyle().Foreground(mg.BrightGold).Italic(true)
		currentInfo = currentStyle.Render(fmt.Sprintf(" %s %s", mg.SymWorking, h.CurrentIssueID))
	}

	bar := h.renderProgressBar(total, len(h.Groups[data.ParadePastTheStand]), 20)

	titleLine := lipgloss.JoinHorizontal(
		lipgloss.Center,
		title,
		counts,
		currentInfo,
		workerInfo,
		"  ",
		bar,
	)

	// Pad to full width
	titleLine = lipgloss.NewStyle().Width(h.Width).Render(titleLine)

	beadStr := h.renderBeadString()

	return lipgloss.JoinVertical(lipgloss.Left, titleLine, beadStr)
}

// renderBeadString creates the decorative bead string separator with shimmer animation.
func (h Header) renderBeadString() string {
	beads := []string{mg.BeadRound, mg.BeadDiamond}

	var parts []string
	visibleWidth := 0
	ci := 0
	for visibleWidth < h.Width-2 {
		bead := beads[ci%2]
		parts = append(parts, bead)
		visibleWidth++
		if visibleWidth < h.Width-2 {
			parts = append(parts, mg.BeadDash)
			visibleWidth++
		}
		ci++
	}

	rawString := strings.Join(parts, "")

	// Animate with shimmer when offset is non-zero, static gradient otherwise
	var gradientString string
	if h.BeadOffset > 0 {
		// Offset cycles through 0.0-1.0 over ~20 ticks (10s at 500ms interval)
		phase := float64(h.BeadOffset%20) / 20.0
		gradientString = mg.ApplyShimmerGradient(rawString, phase)
	} else {
		gradientString = mg.ApplyMardiGrasGradient(rawString)
	}

	return lipgloss.NewStyle().Width(h.Width).Render(gradientString)
}

func (h Header) renderProgressBar(total, done, length int) string {
	if total == 0 {
		return ""
	}
	filledLen := int((float64(done) / float64(total)) * float64(length))
	emptyLen := length - filledLen

	filled := strings.Repeat("█", filledLen)
	empty := strings.Repeat("█", emptyLen) // Or "━"

	percent := int((float64(done) / float64(total)) * 100)

	styledFilled := mg.ApplyPartialMardiGrasGradient(filled, length)
	styledEmpty := lipgloss.NewStyle().Foreground(mg.DimPurple).Render(empty)

	textRight := mg.HeaderCounts.Render(fmt.Sprintf(" %d%%", percent))

	return styledFilled + styledEmpty + textRight
}
