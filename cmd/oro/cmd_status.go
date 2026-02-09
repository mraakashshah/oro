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

// statusResponse mirrors the dispatcher's status JSON structure.
// Defined here to avoid coupling the CLI to the dispatcher package internals.
type statusResponse struct {
	State       string            `json:"state"`
	WorkerCount int               `json:"worker_count"`
	QueueDepth  int               `json:"queue_depth"`
	Assignments map[string]string `json:"assignments"`
	FocusedEpic string            `json:"focused_epic,omitempty"`
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
				return err
			}

			w := cmd.OutOrStdout()

			status, pid, err := DaemonStatus(pidPath)
			if err != nil {
				return err
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

// formatStatusResponse writes a human-readable status summary.
func formatStatusResponse(w io.Writer, resp *statusResponse) {
	fmt.Fprintf(w, "  state:       %s\n", resp.State)
	fmt.Fprintf(w, "  workers:     %d\n", resp.WorkerCount)
	fmt.Fprintf(w, "  queue depth: %d\n", resp.QueueDepth)

	if resp.FocusedEpic != "" {
		fmt.Fprintf(w, "  focus:       %s\n", resp.FocusedEpic)
	}

	if len(resp.Assignments) > 0 {
		fmt.Fprintln(w, "  active beads:")
		// Sort worker IDs for deterministic output.
		workers := make([]string, 0, len(resp.Assignments))
		for wID := range resp.Assignments {
			workers = append(workers, wID)
		}
		sort.Strings(workers)
		for _, wID := range workers {
			fmt.Fprintf(w, "    %s -> %s\n", wID, resp.Assignments[wID])
		}
	}
}
