package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"oro/pkg/factoryhealth"

	"github.com/spf13/cobra"
)

// workerStatus holds per-worker health info from the enriched dispatcher response.
type workerStatus struct {
	ID                string  `json:"id"`
	State             string  `json:"state"`
	BeadID            string  `json:"bead_id,omitempty"`
	LastProgressSecs  float64 `json:"last_progress_secs"`
	LastHeartbeatSecs float64 `json:"last_heartbeat_secs,omitempty"`
	Managed           bool    `json:"managed"`
	SpawnFor          bool    `json:"spawn_for,omitempty"`
	TargetBeadID      string  `json:"target_bead_id,omitempty"`
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
	MaxWorkers          int            `json:"max_workers"`
	ManagedCount        int            `json:"managed_count"`
	UnmanagedCount      int            `json:"unmanaged_count"`
	PendingWorkerCount  int            `json:"pending_worker_count"`
	UptimeSeconds       float64        `json:"uptime_seconds"`
	PendingHandoffCount int            `json:"pending_handoff_count"`
	AttemptCounts       map[string]int `json:"attempt_counts,omitempty"`
	ProgressTimeoutSecs float64        `json:"progress_timeout_secs"`

	// QG incidents
	QGFailureIncidentsOpen       int                          `json:"qg_failure_incidents_open"`
	QGFailureOccurrences30m      int                          `json:"qg_failure_occurrences_30m"`
	QGFailureTopFingerprints     []string                     `json:"qg_failure_top_fingerprints,omitempty"`
	AssignmentFrozenByQuarantine bool                         `json:"assignment_frozen_by_quarantine"`
	BlockingRecoveryQuarantines  int                          `json:"blocking_recovery_quarantines,omitempty"`
	AssignmentFreezeReason       string                       `json:"assignment_freeze_reason,omitempty"`
	Health                       *factoryhealth.FactoryHealth `json:"health,omitempty"`
}

// statusSocketTimeout is how long to wait for the dispatcher socket round-trip.
const statusSocketTimeout = 3 * time.Second

// newStatusCmd creates the "oro status" subcommand.
func newStatusCmd() *cobra.Command {
	var jsonOut bool
	var verbose bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current swarm state",
		Long:  "Displays dispatcher status, worker count and active tasks,\nmanager state, and task summary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatusCommand(cmd.Context(), cmd.OutOrStdout(), jsonOut, verbose)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit status as JSON")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show extended status")
	return cmd
}

func runStatusCommand(ctx context.Context, w io.Writer, jsonOut, verbose bool) error {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	status, pid, err := DaemonStatus(paths.PIDPath, paths.SocketPath)
	if err != nil {
		return fmt.Errorf("get daemon status: %w", err)
	}
	switch status {
	case StatusRunning:
		return printRunningStatus(ctx, w, paths.StateDBPath, pid, jsonOut, verbose)
	case StatusStale:
		return printLocalStatus(ctx, w, paths.StateDBPath, string(StatusStale), pid, jsonOut)
	case StatusStopped:
		return printLocalStatus(ctx, w, paths.StateDBPath, string(StatusStopped), 0, jsonOut)
	default:
		return nil
	}
}

func printRunningStatus(ctx context.Context, w io.Writer, stateDBPath string, pid int, jsonOut, verbose bool) error {
	resp, fetchOK := fetchDispatcherStatusForDisplay(ctx, w)
	if jsonOut {
		if !fetchOK {
			return printStatusJSONFromLocalHealth(ctx, w, stateDBPath, string(StatusRunning), pid, true)
		}
		formatStatusJSON(w, resp)
		return nil
	}
	fmt.Fprintf(w, "dispatcher: running (PID %d)\n", pid)
	if !fetchOK {
		return nil
	}
	if verbose {
		formatStatusVerbose(w, resp)
	} else {
		formatStatusResponse(w, resp)
	}
	return nil
}

func printLocalStatus(ctx context.Context, w io.Writer, stateDBPath, state string, pid int, jsonOut bool) error {
	if jsonOut {
		return printStatusJSONFromLocalHealth(ctx, w, stateDBPath, state, pid, false)
	}
	if state == string(StatusStale) {
		fmt.Fprintf(w, "dispatcher: stale (PID %d, process dead)\n", pid)
		return nil
	}
	fmt.Fprintln(w, "dispatcher: stopped")
	return nil
}

func printStatusJSONFromLocalHealth(ctx context.Context, w io.Writer, stateDBPath, state string, pid int, daemonRunning bool) error {
	health, err := loadLocalFactoryHealth(ctx, stateDBPath, daemonRunning, pid, state)
	if err != nil {
		return fmt.Errorf("load local factory health: %w", err)
	}
	formatStatusJSON(w, &statusResponse{
		State:                   state,
		PID:                     pid,
		QueueDepth:              health.Metrics.ReadyQueue,
		QGFailureIncidentsOpen:  health.Metrics.OpenQGIncidents,
		QGFailureOccurrences30m: health.Metrics.QGOccurrences30m,
		Health:                  &health,
	})
	return nil
}

func fetchDispatcherStatusForDisplay(ctx context.Context, w io.Writer) (*statusResponse, bool) {
	resp, err := fetchDispatcherStatus(ctx)
	if err != nil {
		fmt.Fprintln(w, "  dispatcher detail unavailable")
		return nil, false
	}
	return resp, true
}

func fetchDispatcherStatus(ctx context.Context) (*statusResponse, error) {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	return fetchDispatcherStatusAt(ctx, paths.SocketPath)
}

func fetchDispatcherStatusAt(ctx context.Context, sockPath string) (*statusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, statusSocketTimeout)
	defer cancel()

	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial dispatcher: %w", err)
	}
	defer conn.Close()

	if err := sendDirective(conn, "status", ""); err != nil {
		return nil, fmt.Errorf("send status directive: %w", err)
	}

	ack, err := readACK(conn)
	if err != nil {
		return nil, fmt.Errorf("read status ack: %w", err)
	}

	resp, err := parseStatusFromACK(ack.Detail)
	if err != nil {
		return nil, err
	}
	return resp, nil
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

	progressTimeout := effectiveProgressTimeout(resp.ProgressTimeoutSecs)
	halfTimeout := progressTimeout / 2
	for _, ws := range resp.Workers {
		if ws.State != "busy" || ws.LastProgressSecs <= 0 {
			continue
		}
		if ws.LastProgressSecs >= progressTimeout {
			if ws.LastHeartbeatSecs > 0 && ws.LastHeartbeatSecs < progressTimeout {
				alerts = append(alerts, alert{"!", fmt.Sprintf("%s: alive_no_progress (%s)", ws.ID, formatDuration(ws.LastProgressSecs))})
			} else {
				alerts = append(alerts, alert{"!", fmt.Sprintf("%s: no progress (%s) CRITICAL", ws.ID, formatDuration(ws.LastProgressSecs))})
			}
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
	if resp.QGFailureIncidentsOpen > 0 {
		alerts = append(alerts, alert{"!", fmt.Sprintf("%d open QG failure incident(s)", resp.QGFailureIncidentsOpen)})
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

func effectiveProgressTimeout(secs float64) float64 {
	if secs > 0 {
		return secs
	}
	return 600
}

// filterActiveWorkers returns workers in busy or reviewing state.
func filterActiveWorkers(workers []workerStatus) []workerStatus {
	var active []workerStatus
	for _, ws := range workers {
		if ws.State == "busy" || ws.State == "reviewing" {
			active = append(active, ws)
		}
	}
	return active
}

// formatQGIncidents writes the QG incidents section if any are present.
func formatQGIncidents(w io.Writer, resp *statusResponse) {
	if resp.QGFailureIncidentsOpen == 0 {
		return
	}

	fmt.Fprintf(w, "  QG incidents: %d open", resp.QGFailureIncidentsOpen)
	if resp.QGFailureOccurrences30m > 0 {
		fmt.Fprintf(w, " (%d total occurrences)", resp.QGFailureOccurrences30m)
	}
	fmt.Fprintln(w)

	if len(resp.QGFailureTopFingerprints) > 0 {
		fmt.Fprintln(w, "    top fingerprints:")
		for _, fp := range resp.QGFailureTopFingerprints {
			fmt.Fprintf(w, "      %s\n", fp)
		}
	}
}

// formatStatusResponse writes a human-readable status summary with alerts.
func formatStatusResponse(w io.Writer, resp *statusResponse) {
	formatAlerts(w, resp)

	formatStatusHealthSummary(w, resp.Health)
	formatStatusOpsRuns(w, resp.Health)

	fmt.Fprintf(w, "  state:       %s\n", resp.State)

	activeCount := resp.ActiveCount
	idleCount := resp.IdleCount
	if len(resp.Workers) > 0 {
		activeCount = len(filterActiveWorkers(resp.Workers))
		idleCount = len(resp.Workers) - activeCount
	}
	fmt.Fprintf(w, "  workers:     %d active, %d idle (target: %d", activeCount, idleCount, resp.TargetCount)
	if resp.MaxWorkers > 0 || resp.ManagedCount > 0 || resp.UnmanagedCount > 0 || resp.PendingWorkerCount > 0 {
		fmt.Fprintf(w, ", max: %d, managed: %d, manual: %d, pending: %d",
			resp.MaxWorkers, resp.ManagedCount, resp.UnmanagedCount, resp.PendingWorkerCount)
	}
	fmt.Fprintln(w, ")")

	fmt.Fprintf(w, "  queue:       %d ready\n", resp.QueueDepth)

	if resp.FocusedEpic != "" {
		fmt.Fprintf(w, "  focus:       %s\n", resp.FocusedEpic)
	}

	formatQGIncidents(w, resp)

	switch {
	case len(resp.Workers) > 0:
		formatInProgressBeads(w, resp)
	case len(resp.Assignments) > 0:
		// Legacy fallback: flat assignments map.
		fmt.Fprintln(w, "  in_progress tasks:")
		ids := make([]string, 0, len(resp.Assignments))
		for wID := range resp.Assignments {
			ids = append(ids, wID)
		}
		sort.Strings(ids)
		for _, wID := range ids {
			fmt.Fprintf(w, "    %s -> %s\n", wID, resp.Assignments[wID])
		}
	default:
		fmt.Fprintln(w, "  in_progress tasks: none")
	}
}

func formatStatusHealthSummary(w io.Writer, health *factoryhealth.FactoryHealth) {
	if health == nil {
		return
	}
	fmt.Fprintf(w, "  health:      %s (%s)\n", health.State, health.Posture)
	for i, finding := range health.Findings {
		if i >= 3 {
			fmt.Fprintf(w, "    ... %d more finding(s)\n", len(health.Findings)-i)
			break
		}
		fmt.Fprintf(w, "    %s: %s\n", finding.Code, finding.Message)
	}
}

func formatStatusOpsRuns(w io.Writer, health *factoryhealth.FactoryHealth) {
	if health == nil {
		return
	}
	metrics := health.Metrics.OpsRuns
	if metrics.Running+metrics.Failed+metrics.Stale == 0 {
		return
	}
	fmt.Fprintf(w, "  ops runs:   %s\n", formatOpsRunCounts(metrics.Running, metrics.Failed, metrics.Stale))

	if len(metrics.ByType) > 0 {
		types := make([]string, 0, len(metrics.ByType))
		for runType := range metrics.ByType {
			types = append(types, runType)
		}
		sort.Strings(types)
		for _, runType := range types {
			counts := metrics.ByType[runType]
			fmt.Fprintf(w, "    %s: %s\n", runType, formatOpsRunCounts(counts.Running, counts.Failed, counts.Stale))
		}
	}

	for _, run := range metrics.Runs {
		if run.Status != "failed" && run.Status != "stale" {
			continue
		}
		fmt.Fprintf(w, "    #%d %s %s %s", run.ID, run.Type, run.BeadID, run.Status)
		if run.AgeSecs > 0 {
			fmt.Fprintf(w, " (%s ago)", formatDuration(run.AgeSecs))
		}
		fmt.Fprintf(w, " | action: %s\n", opsRunAction(health, run))
	}
}

func formatOpsRunCounts(running, failed, stale int) string {
	parts := make([]string, 0, 3)
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", stale))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func opsRunAction(health *factoryhealth.FactoryHealth, run factoryhealth.OpsRunSnapshot) string {
	code := factoryhealth.FindingOpsRunFailed
	if run.Status == "stale" {
		code = factoryhealth.FindingOpsRunStale
	}
	for _, finding := range health.Findings {
		if finding.Code != code {
			continue
		}
		if finding.Type != "" && finding.Type != run.Type {
			continue
		}
		if finding.BeadID != "" && finding.BeadID != run.BeadID {
			continue
		}
		if finding.RecommendedAction != "" {
			return finding.RecommendedAction
		}
	}
	return factoryhealth.OpsRunRecommendedAction(run)
}

// formatInProgressBeads writes the in-progress beads section using enriched worker data.
func formatInProgressBeads(w io.Writer, resp *statusResponse) {
	// Filter to busy workers only.
	var busy []workerStatus
	for _, ws := range resp.Workers {
		if ws.BeadID != "" {
			busy = append(busy, ws)
		}
	}
	if len(busy) == 0 {
		fmt.Fprintln(w, "  in_progress tasks: none")
		return
	}

	sort.Slice(busy, func(i, j int) bool { return busy[i].ID < busy[j].ID })

	fmt.Fprintln(w, "  in_progress tasks:")
	progressTimeout := effectiveProgressTimeout(resp.ProgressTimeoutSecs)
	halfTimeout := progressTimeout / 2
	for _, ws := range busy {
		health := "healthy"
		if ws.LastProgressSecs >= progressTimeout {
			if ws.LastHeartbeatSecs > 0 && ws.LastHeartbeatSecs < progressTimeout {
				health = "alive_no_progress"
			} else {
				health = "STUCK"
			}
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
