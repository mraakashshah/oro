package main

import (
	"context"
	"fmt"
	"strings"

	"oro/pkg/eventlog"
)

// WorkerEvent represents a worker event for display purposes.
type WorkerEvent struct {
	Type      string
	Timestamp string
	BeadID    string
	Payload   string
}

// fetchWorkerEvents retrieves the most recent worker events for a given worker ID.
// Returns empty slice if database is not accessible or worker has no events.
func fetchWorkerEvents(ctx context.Context, workerID string, limit int) ([]WorkerEvent, error) {
	if workerID == "" {
		return nil, nil
	}

	dbPath := eventlog.DefaultDBPath()
	reader, err := eventlog.NewReader(dbPath)
	if err != nil {
		// Database not accessible - return empty slice (daemon may be offline)
		return nil, nil
	}
	defer reader.Close()

	events, err := reader.QueryWorkerEvents(ctx, eventlog.QueryOpts{
		WorkerID: workerID,
		Limit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query worker events: %w", err)
	}

	// Convert to display format
	result := make([]WorkerEvent, len(events))
	for i, e := range events {
		result[i] = WorkerEvent{
			Type:      e.Type,
			Timestamp: e.CreatedAt.Format("15:04:05"),
			BeadID:    e.BeadID,
			Payload:   e.Payload,
		}
	}

	return result, nil
}

// renderWorkerEvents renders worker event history as a table.
func renderWorkerEvents(events []WorkerEvent, _ Theme, styles Styles) string {
	if len(events) == 0 {
		return styles.DetailDimItalic.Render("No events")
	}

	var lines []string
	lines = append(lines, styles.Primary.Bold(true).Render("Recent Events:"), "")

	// Column widths
	const timeWidth = 8
	const typeWidth = 20
	const beadWidth = 15

	// Header
	header := fmt.Sprintf("%-*s  %-*s  %-*s",
		timeWidth, "Time",
		typeWidth, "Event",
		beadWidth, "Bead")
	lines = append(lines,
		styles.Bold.Render(header),
		strings.Repeat("─", 50))

	// Events
	for _, e := range events {
		eventType := truncate(e.Type, typeWidth)
		beadID := truncate(e.BeadID, beadWidth)

		row := fmt.Sprintf("%-*s  %-*s  %-*s",
			timeWidth, e.Timestamp,
			typeWidth, eventType,
			beadWidth, beadID)

		lines = append(lines, row)
	}

	return strings.Join(lines, "\n")
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
