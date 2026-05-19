package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

type opsCommandOptions struct {
	jsonOut bool
}

type opsRunView struct {
	ID            int64  `json:"id"`
	EscalationID  int64  `json:"escalation_id,omitempty"`
	Type          string `json:"type"`
	BeadID        string `json:"bead_id,omitempty"`
	WorkerID      string `json:"worker_id,omitempty"`
	DispatcherPID int    `json:"dispatcher_pid,omitempty"`
	ProcessPID    int    `json:"process_pid,omitempty"`
	Runtime       string `json:"runtime,omitempty"`
	Model         string `json:"model,omitempty"`
	Status        string `json:"status"`
	Verdict       string `json:"verdict,omitempty"`
	Feedback      string `json:"feedback,omitempty"`
	Error         string `json:"error,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

type opsRetryView struct {
	ID          int64  `json:"id"`
	Retried     bool   `json:"retried"`
	Status      string `json:"status"`
	NewOpsRunID int64  `json:"new_ops_run_id,omitempty"`
	Routed      bool   `json:"routed,omitempty"`
}

type opsResolveView struct {
	ID       int64  `json:"id"`
	Resolved bool   `json:"resolved"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

const maxOpsRunDetailLen = 240

// newOpsCmd creates the "oro ops" operator command.
func newOpsCmd() *cobra.Command {
	opts := &opsCommandOptions{}
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Inspect and recover dispatcher ops runs",
	}
	cmd.PersistentFlags().BoolVar(&opts.jsonOut, "json", false, "emit machine-readable JSON")

	cmd.AddCommand(
		newOpsListCmd(opts),
		newOpsRetryCmd(opts),
		newOpsResolveCmd(opts),
	)
	return cmd
}

func newOpsListCmd(opts *opsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List durable ops runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			detail, err := runOpsDirective(cmd.Context(), protocol.DirectiveOpsRuns, "")
			if err != nil {
				return err
			}
			if opts.jsonOut {
				writeJSONDetail(cmd.OutOrStdout(), detail)
				return nil
			}
			return formatOpsRuns(cmd.OutOrStdout(), detail)
		},
	}
}

func newOpsRetryCmd(opts *opsCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <run-id>",
		Short: "Supersede and retry a failed or stale ops run",
		Args:  requireOpsRunIDArg("retry"),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseOpsCLIID(args[0], "retry")
			if err != nil {
				return err
			}
			detail, err := runOpsDirective(cmd.Context(), protocol.DirectiveOpsRetry, strconv.FormatInt(runID, 10))
			if err != nil {
				return err
			}
			if opts.jsonOut {
				writeJSONDetail(cmd.OutOrStdout(), detail)
				return nil
			}
			return formatOpsRetry(cmd.OutOrStdout(), detail)
		},
	}
}

func newOpsResolveCmd(opts *opsCommandOptions) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "resolve <run-id>",
		Short: "Resolve an ops run after validating the underlying condition",
		Args:  requireOpsRunIDArg("resolve"),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := parseOpsCLIID(args[0], "resolve")
			if err != nil {
				return err
			}
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return fmt.Errorf("ops resolve requires --reason")
			}
			detail, err := runOpsDirective(cmd.Context(), protocol.DirectiveOpsResolve, fmt.Sprintf("%d %s", runID, reason))
			if err != nil {
				return err
			}
			if opts.jsonOut {
				writeJSONDetail(cmd.OutOrStdout(), detail)
				return nil
			}
			return formatOpsResolve(cmd.OutOrStdout(), detail)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason for resolving the run")
	return cmd
}

func runOpsDirective(ctx context.Context, directive protocol.Directive, args string) (string, error) {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return "", fmt.Errorf("resolve paths: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, statusSocketTimeout)
	defer cancel()

	conn, err := dialDispatcher(ctx, paths.SocketPath)
	if err != nil {
		return "", fmt.Errorf("daemon unavailable: run 'oro status' for state or 'oro start --daemon-only' to start the dispatcher: %w", err)
	}
	defer conn.Close()

	if err := sendDirective(conn, string(directive), args); err != nil {
		return "", fmt.Errorf("send %s directive: %w", directive, err)
	}
	ack, err := readACK(conn)
	if err != nil {
		return "", err
	}
	return ack.Detail, nil
}

func parseOpsCLIID(raw, op string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("ops %s requires a numeric run id", op)
	}
	return id, nil
}

func requireOpsRunIDArg(op string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("ops %s requires a run id", op)
		}
		if len(args) > 1 {
			return fmt.Errorf("ops %s accepts exactly one run id", op)
		}
		return nil
	}
}

func writeJSONDetail(w io.Writer, detail string) {
	fmt.Fprintln(w, strings.TrimSpace(detail))
}

func formatOpsRuns(w io.Writer, detail string) error {
	var runs []opsRunView
	if err := json.Unmarshal([]byte(detail), &runs); err != nil {
		return fmt.Errorf("parse ops-runs response: %w", err)
	}
	if len(runs) == 0 {
		fmt.Fprintln(w, "ops runs: none")
		return nil
	}
	fmt.Fprintln(w, "ID\tTYPE\tBEAD\tSTATUS\tWORKER\tDETAIL")
	for _, run := range runs {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", run.ID, run.Type, run.BeadID, run.Status, run.WorkerID, opsRunDetail(run))
	}
	return nil
}

func formatOpsRetry(w io.Writer, detail string) error {
	var resp opsRetryView
	if err := json.Unmarshal([]byte(detail), &resp); err != nil {
		return fmt.Errorf("parse ops-retry response: %w", err)
	}
	if !resp.Retried {
		fmt.Fprintf(w, "ops run %d not retried: status=%s\n", resp.ID, resp.Status)
		return nil
	}
	fmt.Fprintf(w, "ops run %d retried: status=%s new_ops_run_id=%d routed=%t\n", resp.ID, resp.Status, resp.NewOpsRunID, resp.Routed)
	return nil
}

func formatOpsResolve(w io.Writer, detail string) error {
	var resp opsResolveView
	if err := json.Unmarshal([]byte(detail), &resp); err != nil {
		return fmt.Errorf("parse ops-resolve response: %w", err)
	}
	if !resp.Resolved {
		fmt.Fprintf(w, "ops run %d not resolved: status=%s\n", resp.ID, resp.Status)
		return nil
	}
	if resp.Reason == "" {
		fmt.Fprintf(w, "ops run %d resolved: status=%s\n", resp.ID, resp.Status)
		return nil
	}
	fmt.Fprintf(w, "ops run %d resolved: status=%s reason=%q\n", resp.ID, resp.Status, resp.Reason)
	return nil
}

func opsRunDetail(run opsRunView) string {
	return truncateOpsRunDetail(opsRunFullDetail(run))
}

func opsRunFullDetail(run opsRunView) string {
	switch {
	case run.Error != "":
		return run.Error
	case run.Feedback != "":
		return run.Feedback
	case run.Verdict != "":
		return run.Verdict
	default:
		return "-"
	}
}

func truncateOpsRunDetail(detail string) string {
	if len(detail) <= maxOpsRunDetailLen {
		return detail
	}
	return detail[:maxOpsRunDetailLen] + "... (truncated)"
}
