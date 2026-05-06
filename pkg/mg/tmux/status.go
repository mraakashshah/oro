package tmux

import (
	"fmt"

	"oro/pkg/mg"
	"oro/pkg/mg/data"
)

// tmux 256-color equivalents for the parade theme.
const (
	colourRolling = "colour42"  // BrightGreen #2ECC71
	colourLinedUp = "colour220" // BrightGold  #FFD700
	colourStalled = "colour196" // Red         #E74C3C
	colourPassed  = "colour244" // Muted       #888888
	colourFleur   = "colour134" // Purple      #7B2D8E
)

// statusLine returns a tmux-formatted status string showing parade counts.
// Output uses tmux #[fg=colourN] markup — no lipgloss dependency.
func statusLine(groups map[data.ParadeStatus][]data.Issue) string {
	rolling := len(groups[data.ParadeRolling])
	linedUp := len(groups[data.ParadeLinedUp])
	stalled := len(groups[data.ParadeStalled])
	passed := len(groups[data.ParadePastTheStand])

	return fmt.Sprintf(
		"#[fg=%s]%s #[fg=%s]%d%s #[fg=%s]%d%s #[fg=%s]%d%s #[fg=%s]%d%s",
		colourFleur, mg.FleurDeLis,
		colourRolling, rolling, mg.SymRolling,
		colourLinedUp, linedUp, mg.SymLinedUp,
		colourStalled, stalled, mg.SymStalled,
		colourPassed, passed, mg.SymPassed,
	)
}
