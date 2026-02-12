// Package main implements the oro-dash interactive dashboard.
package main

import (
	"encoding/json"
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

// WorkerStatus represents worker status.
type WorkerStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// robotMode outputs a JSON snapshot of beads and workers.
func robotMode(beads []Bead, workers []WorkerStatus) ([]byte, error) {
	snapshot := map[string]any{
		"beads":   beads,
		"workers": workers,
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal snapshot: %w", err)
	}
	return data, nil
}

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running dashboard: %v\n", err)
		os.Exit(1)
	}
}
