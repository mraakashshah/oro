package main

import (
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestOverviewTabShowsDescriptionAndOwner verifies that the Overview tab renders
// Description and Owner fields when present, and omits them when empty.
func TestOverviewTabShowsDescriptionAndOwner(t *testing.T) {
	t.Run("description is rendered when present", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:          "oro-test.desc.1",
			Title:       "Bead with description",
			Description: "some desc",
		}

		theme := DefaultTheme()
		styles := NewStyles(theme)
		model := newDetailModel(bead, theme, styles)
		output := model.renderOverviewTab(styles)

		if !strings.Contains(output, "some desc") {
			t.Errorf("expected 'some desc' in overview output, got:\n%s", output)
		}
	})

	t.Run("owner is rendered when present", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.owner.1",
			Title: "Bead with owner",
			Owner: "alice",
		}

		theme := DefaultTheme()
		styles := NewStyles(theme)
		model := newDetailModel(bead, theme, styles)
		output := model.renderOverviewTab(styles)

		if !strings.Contains(output, "Owner: alice") {
			t.Errorf("expected 'Owner: alice' in overview output, got:\n%s", output)
		}
	})

	t.Run("empty description is omitted", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:          "oro-test.desc.empty",
			Title:       "Bead without description",
			Description: "",
		}

		theme := DefaultTheme()
		styles := NewStyles(theme)
		model := newDetailModel(bead, theme, styles)
		outputWithout := model.renderOverviewTab(styles)

		beadWith := protocol.BeadDetail{
			ID:          "oro-test.desc.present",
			Title:       "Bead with description",
			Description: "has a description",
		}
		modelWith := newDetailModel(beadWith, theme, styles)
		outputWith := modelWith.renderOverviewTab(styles)

		// With description, output should be longer (description section added)
		if len(outputWith) <= len(outputWithout) {
			t.Errorf("expected output with description to be longer than without\nWith: %s\nWithout: %s", outputWith, outputWithout)
		}
	})

	t.Run("empty owner is omitted", func(t *testing.T) {
		bead := protocol.BeadDetail{
			ID:    "oro-test.owner.empty",
			Title: "Bead without owner",
			Owner: "",
		}

		theme := DefaultTheme()
		styles := NewStyles(theme)
		model := newDetailModel(bead, theme, styles)
		output := model.renderOverviewTab(styles)

		if strings.Contains(output, "Owner:") {
			t.Errorf("expected 'Owner:' to be absent when owner is empty, got:\n%s", output)
		}
	})
}
