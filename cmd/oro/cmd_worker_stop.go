package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// newWorkerStopCmd creates the "oro worker stop" subcommand.
func newWorkerStopCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "stop [id]",
		Short: "Stop a connected worker",
		Long: `Stops one or all connected workers by sending a kill-worker directive to the dispatcher.

With a worker ID: sends kill-worker <id> and prints the result.
With --all: queries the dispatcher for all connected worker IDs, then sends
kill-worker for each one.

Cleanup (worktree removal, bead reset to open) is handled by the dispatcher's
applyKillWorker handler.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var workerID string
			if len(args) > 0 {
				workerID = args[0]
			}
			if !all && workerID == "" {
				return fmt.Errorf("either a worker ID or --all is required")
			}
			if all && workerID != "" {
				return fmt.Errorf("cannot specify both a worker ID and --all")
			}
			return runWorkerStop(cmd.OutOrStdout(), workerID, all)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "stop all connected workers")

	return cmd
}

// runWorkerStop implements the core logic for "oro worker stop".
// workerID is the ID to kill (used when all=false).
// all=true queries status first and kills every connected worker.
func runWorkerStop(w io.Writer, workerID string, all bool) error {
	paths, err := ResolvePaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	sockPath := paths.SocketPath

	if all {
		return runWorkerStopAll(w, sockPath)
	}
	return runWorkerStopOne(w, sockPath, workerID)
}

// runWorkerStopOne sends a single kill-worker directive for the given worker ID.
func runWorkerStopOne(w io.Writer, sockPath, workerID string) error {
	conn, err := dialDispatcher(context.Background(), sockPath)
	if err != nil {
		return fmt.Errorf("connect to dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, string(protocol.DirectiveKillWorker), workerID); err != nil {
		return fmt.Errorf("send kill-worker directive: %w", err)
	}

	ack, err := readACK(conn)
	if err != nil {
		return fmt.Errorf("kill-worker %s: %w", workerID, err)
	}

	if ack.Detail != "" {
		fmt.Fprintln(w, ack.Detail)
	} else {
		fmt.Fprintf(w, "worker %s stopped\n", workerID)
	}
	return nil
}

// runWorkerStopAll queries the dispatcher for connected worker IDs, then kills each one.
func runWorkerStopAll(w io.Writer, sockPath string) error {
	workerIDs, err := fetchConnectedWorkerIDs(sockPath)
	if err != nil {
		return fmt.Errorf("query dispatcher status: %w", err)
	}

	if len(workerIDs) == 0 {
		fmt.Fprintln(w, "no workers connected")
		return nil
	}

	var lastErr error
	for _, id := range workerIDs {
		if err := runWorkerStopOne(w, sockPath, id); err != nil {
			fmt.Fprintf(w, "warning: stop %s: %v\n", id, err)
			lastErr = err
		}
	}
	return lastErr
}

// workerListResponse is the subset of the dispatcher's status JSON we need.
type workerListResponse struct {
	Workers []struct {
		ID string `json:"id"`
	} `json:"workers"`
}

// fetchConnectedWorkerIDs sends a status directive and parses the workers list.
func fetchConnectedWorkerIDs(sockPath string) ([]string, error) {
	conn, err := dialDispatcher(context.Background(), sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, string(protocol.DirectiveStatus), ""); err != nil {
		return nil, fmt.Errorf("send status directive: %w", err)
	}

	ack, err := readACK(conn)
	if err != nil {
		return nil, fmt.Errorf("status ack: %w", err)
	}

	var resp workerListResponse
	if err := json.Unmarshal([]byte(ack.Detail), &resp); err != nil {
		return nil, fmt.Errorf("parse status response: %w", err)
	}

	ids := make([]string, 0, len(resp.Workers))
	for _, w := range resp.Workers {
		if w.ID != "" {
			ids = append(ids, w.ID)
		}
	}
	return ids, nil
}
