package main

import "fmt"

// renderFocusLine returns the "Focused: <id> (<title>)" status line for a
// focused epic. Returns an empty string when epicID is empty (no focus set).
// titleFn is called to resolve the epic title; if it returns "" the id is
// shown without a title suffix.
func renderFocusLine(epicID string, titleFn func(string) string) string {
	if epicID == "" {
		return ""
	}
	title := titleFn(epicID)
	if title == "" {
		return fmt.Sprintf("Focused: %s", epicID)
	}
	return fmt.Sprintf("Focused: %s (%s)", epicID, title)
}
