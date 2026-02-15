package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// workerStatus holds per-worker health info from the enriched dispatcher response.
type workerStatus struct {
	ID               string  `json:"id"`
	State            string  `json:"state"`
	BeadID           string  `json:"bead_id,omitempty"`
	LastProgressSecs float64 `json:"last_progress_secs"`
}

// statusResponse mirrors the dispatcher's status JSON structure.
// Defined here to avoid coupling the CLI to the dispatcher package internals.
type statusResponse struct {
	State       string            `json:"state"`
	PID         int               `json:"pid"`
	WorkerCount int               `json:"worker_count"`
	QueueDepth  int               `json:"queue_depth"`
	Assignments map[string]string `json:"assignments"`
	FocusedEpic string            `json:"focused_epic,omitempty"`

	// Enriched fields (oro-vii8.1)
	Workers             []workerStatus `json:"workers"`
	ActiveCount         int            `json:"active_count"`
	IdleCount           int            `json:"idle_count"`
	TargetCount         int            `json:"target_count"`
	UptimeSeconds       float64        `json:"uptime_seconds"`
	PendingHandoffCount int            `json:"pending_handoff_count"`
	AttemptCounts       map[string]int `json:"attempt_counts,omitempty"`
	ProgressTimeoutSecs float64        `json:"progress_timeout_secs"`
}

// statusSocketTimeout is how long to wait for the dispatcher socket round-trip.
const statusSocketTimeout = 3 * time.Second

// newStatusCmd creates the "oro status" subcommand.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current swarm state",
		Long:  "Displays dispatcher status, worker count and active beads,\nmanager state, and bead summary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
			if err != nil {
				return fmt.Errorf("get pid path: %w", err)
			}
			sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
			if err != nil {
				return fmt.Errorf("get socket path: %w", err)
			}

			w := cmd.OutOrStdout()

			status, pid, err := DaemonStatus(pidPath, sockPath)
			if err != nil {
				return fmt.Errorf("get daemon status: %w", err)
			}

			switch status {
			case StatusRunning:
				fmt.Fprintf(w, "dispatcher: running (PID %d)\n", pid)
				queryDispatcherStatus(cmd.Context(), w)
			case StatusStale:
				fmt.Fprintf(w, "dispatcher: stale (PID %d, process dead)\n", pid)
			case StatusStopped:
				fmt.Fprintln(w, "dispatcher: stopped")
			}

			return nil
		},
	}
}

// queryDispatcherStatus connects to the dispatcher socket, sends a status
// directive, and prints the parsed response. On any failure it prints a
// graceful fallback message instead of returning an error.
func queryDispatcherStatus(ctx context.Context, w io.Writer) {
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, statusSocketTimeout)
	defer cancel()

	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return
	}
	defer conn.Close()

	if err := sendDirective(conn, "status", ""); err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return
	}

	ack, err := readACK(conn)
	if err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return
	}

	resp, err := parseStatusFromACK(ack.Detail)
	if err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return
	}

	formatStatusResponse(w, resp)
}

// parseStatusFromACK parses the status JSON from an ACK detail string.
func parseStatusFromACK(detail string) (*statusResponse, error) {
	var resp statusResponse
	if err := json.Unmarshal([]byte(detail), &resp); err != nil {
		return nil, fmt.Errorf("parse status response: %w", err)
	}
	return &resp, nil
}

// formatAlerts writes the ALERTS section if any problems are detected.
// Returns true if any alerts were written.
func formatAlerts(w io.Writer, resp *statusResponse) bool {
	type alert struct {
		icon string
		msg  string
	}
	var alerts []alert

	// Stuck worker alerts: progress exceeds half the timeout.
	halfTimeout := resp.ProgressTimeoutSecs / 2
	for _, ws := range resp.Workers {
		if ws.State != "busy" || ws.LastProgressSecs <= 0 {
			continue
		}
		if ws.LastProgressSecs >= resp.ProgressTimeoutSecs {
			alerts = append(alerts, alert{"!", fmt.Sprintf("%s: no progress (%s) CRITICAL", ws.ID, formatDuration(ws.LastProgressSecs))})
		} else if ws.LastProgressSecs >= halfTimeout {
			alerts = append(alerts, alert{"!", fmt.Sprintf("%s: no progress (%s)", ws.ID, formatDuration(ws.LastProgressSecs))})
		}
	}

	// QG failure alerts: beads with 2+ attempts.
	for beadID, count := range resp.AttemptCounts {
		if count >= 2 {
			alerts = append(alerts, alert{"!", fmt.Sprintf("%s: QG failed %dx", beadID, count)})
		}
	}

	// Pending handoff alerts.
	if resp.PendingHandoffCount > 0 {
		alerts = append(alerts, alert{"!", fmt.Sprintf("%d pending handoff(s)", resp.PendingHandoffCount)})
	}

	if len(alerts) == 0 {
		return false
	}

	sort.Slice(alerts, func(i, j int) bool { return alerts[i].msg < alerts[j].msg })
	fmt.Fprintln(w, "ALERTS:")
	for _, a := range alerts {
		fmt.Fprintf(w, "  %s %s\n", a.icon, a.msg)
	}
	fmt.Fprintln(w)
	return true
}

// formatDuration formats seconds into a human-readable short duration string.
func formatDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// formatStatusResponse writes a human-readable status summary with alerts.
func formatStatusResponse(w io.Writer, resp *statusResponse) {
	formatAlerts(w, resp)

	fmt.Fprintf(w, "  state:       %s\n", resp.State)

	// Use enriched worker counts if available, fall back to legacy.
	if resp.TargetCount > 0 || resp.ActiveCount > 0 || resp.IdleCount > 0 {
		fmt.Fprintf(w, "  workers:     %d active, %d idle (target: %d)\n", resp.ActiveCount, resp.IdleCount, resp.TargetCount)
	} else {
		fmt.Fprintf(w, "  workers:     %d\n", resp.WorkerCount)
	}

	fmt.Fprintf(w, "  queue:       %d ready\n", resp.QueueDepth)

	if resp.FocusedEpic != "" {
		fmt.Fprintf(w, "  focus:       %s\n", resp.FocusedEpic)
	}

	if len(resp.Workers) > 0 {
		formatActiveBeads(w, resp)
	} else if len(resp.Assignments) > 0 {
		// Legacy fallback: flat assignments map.
		fmt.Fprintln(w, "  active beads:")
		ids := make([]string, 0, len(resp.Assignments))
		for wID := range resp.Assignments {
			ids = append(ids, wID)
		}
		sort.Strings(ids)
		for _, wID := range ids {
			fmt.Fprintf(w, "    %s -> %s\n", wID, resp.Assignments[wID])
		}
	}
}

// formatActiveBeads writes the active beads section using enriched worker data.
func formatActiveBeads(w io.Writer, resp *statusResponse) {
	// Filter to busy workers only.
	var busy []workerStatus
	for _, ws := range resp.Workers {
		if ws.BeadID != "" {
			busy = append(busy, ws)
		}
	}
	if len(busy) == 0 {
		return
	}

	sort.Slice(busy, func(i, j int) bool { return busy[i].ID < busy[j].ID })

	fmt.Fprintln(w, "  active beads:")
	halfTimeout := resp.ProgressTimeoutSecs / 2
	for _, ws := range busy {
		health := "healthy"
		if ws.LastProgressSecs >= resp.ProgressTimeoutSecs {
			health = "STUCK"
		} else if ws.LastProgressSecs >= halfTimeout {
			health = "slow"
		}
		fmt.Fprintf(w, "    %s -> %s (%s, %s ago)\n", ws.ID, ws.BeadID, health, formatDuration(ws.LastProgressSecs))
	}
}

// formatStatusJSON writes the status response as JSON.
func formatStatusJSON(w io.Writer, resp *statusResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	fmt.Fprintln(w, string(data))
}

// formatStatusVerbose writes an extended status view including worker health
// table, attempt counts, uptime, and pending handoff info.
func formatStatusVerbose(w io.Writer, resp *statusResponse) {
	// Start with the default view.
	formatStatusResponse(w, resp)

	// Uptime
	fmt.Fprintf(w, "  uptime:      %s\n", formatDuration(resp.UptimeSeconds))

	// Pending handoffs
	fmt.Fprintf(w, "  pending handoffs: %d\n", resp.PendingHandoffCount)

	// Worker health table
	if len(resp.Workers) > 0 {
		sorted := make([]workerStatus, len(resp.Workers))
		copy(sorted, resp.Workers)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

		fmt.Fprintln(w, "  worker health:")
		for _, ws := range sorted {
			progress := "-"
			if ws.LastProgressSecs > 0 {
				progress = formatDuration(ws.LastProgressSecs) + " ago"
			}
			bead := ws.BeadID
			if bead == "" {
				bead = "-"
			}
			fmt.Fprintf(w, "    %-12s %-8s %-16s %s\n", ws.ID, ws.State, bead, progress)
		}
	}

	// Attempt counts per bead
	if len(resp.AttemptCounts) > 0 {
		fmt.Fprintln(w, "  attempt counts:")
		beads := make([]string, 0, len(resp.AttemptCounts))
		for b := range resp.AttemptCounts {
			beads = append(beads, b)
		}
		sort.Strings(beads)
		for _, b := range beads {
			fmt.Fprintf(w, "    %s: %d attempts\n", b, resp.AttemptCounts[b])
		}
	}
}
