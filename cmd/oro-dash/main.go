// Package main implements the oro-dash interactive dashboard.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Bead represents a bead/issue.
type Bead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

// WorkerStatus represents worker status with enriched fields.
type WorkerStatus struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	BeadID           string  `json:"bead_id,omitempty"`
	LastProgressSecs float64 `json:"last_progress_secs"`
	ContextPct       int     `json:"context_pct"`
}

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running dashboard: %v\n", err)
		os.Exit(1)
	}
}
