package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderSparkline renders a Unicode sparkline from a slice of float64 values.
// Values are normalized to the local min/max range and mapped to 8-level Unicode blocks.
// If width > len(values), the sparkline is left-padded with baseline blocks (▁).
// If width < len(values), only the most recent (rightmost) values are shown.
// Empty values or zero width returns an empty string.
//
//nolint:unparam,unused // styles param required by caller contract; will be called by StatusView (oro-yqvn.6)
func renderSparkline(values []float64, width int, color lipgloss.Color, _ Styles) string {
	// 8-level Unicode block characters for sparklines (index 0=lowest, 7=highest).
	blocks := [8]rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	const middleIdx = 3 // Index used when all values are identical (no variance).

	if len(values) == 0 || width <= 0 {
		return ""
	}

	// Truncate to most recent values if width < len(values).
	visible := values
	if len(visible) > width {
		visible = visible[len(visible)-width:]
	}

	// Find local min and max.
	lo, hi := visible[0], visible[0]
	for _, v := range visible[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}

	rng := hi - lo

	// Build the sparkline runes.
	var buf strings.Builder
	buf.Grow(width * 4) // UTF-8 runes can be up to 4 bytes

	// Left-pad with baseline block if width > len(visible).
	pad := width - len(visible)
	for range pad {
		buf.WriteRune(blocks[0])
	}

	// Render each value as a block character.
	for _, v := range visible {
		var idx int
		if rng == 0 {
			// All values identical: use middle block.
			idx = middleIdx
		} else {
			// Normalize to 0..7 range.
			idx = int((v - lo) / rng * 7)
			if idx > 7 {
				idx = 7
			}
		}
		buf.WriteRune(blocks[idx])
	}

	// Apply color styling.
	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(buf.String())
}
