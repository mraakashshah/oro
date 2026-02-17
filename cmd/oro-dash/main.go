// Package main implements the oro-dash interactive dashboard.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"oro/pkg/protocol"

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

// fetchFn is a function that fetches beads; injectable for testing.
type fetchFn func(ctx context.Context) ([]protocol.Bead, error)

// robotModeWithFetch fetches beads using the provided fetch function, marshals
// the result to JSON, and writes it to stdout. Returns an error on fetch failure.
func robotModeWithFetch(ctx context.Context, fetch fetchFn) error {
	beads, err := fetch(ctx)
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Println(string(errJSON))
		return err
	}
	if beads == nil {
		beads = []protocol.Bead{}
	}
	out, err := json.Marshal(beads)
	if err != nil {
		return fmt.Errorf("marshal beads: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// robotMode fetches beads and writes JSON to stdout.
func robotMode() error {
	return robotModeWithFetch(context.Background(), fetchBeads)
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--json" {
			if err := robotMode(); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}

	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running dashboard: %v\n", err)
		os.Exit(1)
	}
}
