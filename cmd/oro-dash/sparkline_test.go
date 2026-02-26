package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSparkline(t *testing.T) {
	styles := NewStyles(DefaultTheme())
	color := lipgloss.Color("#30A46C")

	t.Run("empty values returns empty string", func(t *testing.T) {
		got := renderSparkline(nil, 10, color, styles)
		if got != "" {
			t.Errorf("renderSparkline(nil, 10) = %q, want %q", got, "")
		}

		got = renderSparkline([]float64{}, 10, color, styles)
		if got != "" {
			t.Errorf("renderSparkline([], 10) = %q, want %q", got, "")
		}
	})

	t.Run("all zeros renders all baseline blocks", func(t *testing.T) {
		// All-same-value should render middle block (index 3 = ▄)
		got := renderSparkline([]float64{0, 0, 0}, 3, color, styles)
		// Strip ANSI codes for content check
		plain := stripANSI(got)
		if plain != "▄▄▄" {
			t.Errorf("renderSparkline(all zeros) plain = %q, want %q", plain, "▄▄▄")
		}
	})

	t.Run("all same value renders middle block", func(t *testing.T) {
		got := renderSparkline([]float64{5, 5, 5, 5}, 4, color, styles)
		plain := stripANSI(got)
		if plain != "▄▄▄▄" {
			t.Errorf("renderSparkline(all same) plain = %q, want %q", plain, "▄▄▄▄")
		}
	})

	t.Run("single value renders single block", func(t *testing.T) {
		got := renderSparkline([]float64{42}, 1, color, styles)
		plain := stripANSI(got)
		// Single value = all same = middle block
		if plain != "▄" {
			t.Errorf("renderSparkline(single) plain = %q, want %q", plain, "▄")
		}
	})

	t.Run("normalizes to local min max with 8 levels", func(t *testing.T) {
		// blocks: ▁▂▃▄▅▆▇█
		// min=0, max=7, range=7
		// 0 -> 0 -> ▁
		// 7 -> 7 -> █
		values := []float64{0, 7}
		got := renderSparkline(values, 2, color, styles)
		plain := stripANSI(got)
		if plain != "▁█" {
			t.Errorf("renderSparkline(0,7) plain = %q, want %q", plain, "▁█")
		}
	})

	t.Run("normalized intermediate values", func(t *testing.T) {
		// min=0, max=100, range=100
		// 0 -> index 0 -> ▁
		// 50 -> index 3 (int(50/100*7) = int(3.5) = 3)
		// 100 -> index 7 -> █
		values := []float64{0, 50, 100}
		got := renderSparkline(values, 3, color, styles)
		plain := stripANSI(got)
		if len([]rune(plain)) != 3 {
			t.Errorf("renderSparkline(0,50,100) rune count = %d, want 3", len([]rune(plain)))
		}
		runes := []rune(plain)
		if runes[0] != '▁' {
			t.Errorf("first block = %c, want ▁", runes[0])
		}
		if runes[2] != '█' {
			t.Errorf("last block = %c, want █", runes[2])
		}
	})

	t.Run("width greater than values pads left with baseline", func(t *testing.T) {
		// 2 values but width=5, so 3 baseline blocks on the left
		values := []float64{0, 7}
		got := renderSparkline(values, 5, color, styles)
		plain := stripANSI(got)
		// Expected: "▁▁▁▁█" (3 pad + ▁ + █)
		if plain != "▁▁▁▁█" {
			t.Errorf("renderSparkline(pad left) plain = %q, want %q", plain, "▁▁▁▁█")
		}
	})

	t.Run("width less than values truncates from left", func(t *testing.T) {
		// 5 values: 0,1,2,3,7, width=3 -> take last 3: 2,3,7
		// min=2, max=7, range=5
		// 2 -> 0 -> ▁
		// 3 -> (1/5)*7 = 1.4 -> 1 -> ▂
		// 7 -> 7 -> █
		values := []float64{0, 1, 2, 3, 7}
		got := renderSparkline(values, 3, color, styles)
		plain := stripANSI(got)
		runes := []rune(plain)
		if len(runes) != 3 {
			t.Errorf("renderSparkline(truncate) rune count = %d, want 3", len(runes))
		}
		if runes[0] != '▁' {
			t.Errorf("first block = %c, want ▁", runes[0])
		}
		if runes[2] != '█' {
			t.Errorf("last block = %c, want █", runes[2])
		}
	})

	t.Run("applies color styling returns non-empty", func(t *testing.T) {
		got := renderSparkline([]float64{1, 2, 3}, 3, color, styles)
		// Verify styled output is non-empty and contains the sparkline content.
		// (lipgloss may or may not emit ANSI codes depending on terminal detection.)
		plain := stripANSI(got)
		if len([]rune(plain)) != 3 {
			t.Errorf("renderSparkline styled output rune count = %d, want 3", len([]rune(plain)))
		}
	})

	t.Run("width zero returns empty string", func(t *testing.T) {
		got := renderSparkline([]float64{1, 2, 3}, 0, color, styles)
		if got != "" {
			t.Errorf("renderSparkline(width=0) = %q, want %q", got, "")
		}
	})
}
