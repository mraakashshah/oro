package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"oro/pkg/protocol"
)

func writeBeadListHuman(w io.Writer, beads []protocol.Bead, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tPRI\tTYPE\tUPDATED\tTITLE")
	for _, b := range beads {
		fmt.Fprintf(tw, "%s\t%s\tP%d\t%s\t%s\t%s\n",
			b.ID,
			b.Status,
			b.Priority,
			b.Type,
			beadListUpdatedLabel(now, b),
			singleLineListTitle(b.Title),
		)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush table: %w", err)
	}
	return nil
}

func beadListUpdatedLabel(now time.Time, b protocol.Bead) string {
	ts := b.UpdatedAt
	if ts == "" {
		ts = b.CreatedAt
	}
	if ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return "-"
		}
	}
	diff := now.Sub(t)
	if diff < 0 {
		diff = -diff
	}
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	}
}

func singleLineListTitle(title string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(title)
}
