package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const may11ProductivePerAssignmentBaseline = 0.50

type ThroughputHealth struct {
	WindowStart                   time.Time
	WindowEnd                     time.Time
	Assignments                   int
	ProductiveClosures            int
	DeferredClosures              int
	QGRejections                  int
	ReviewRejections              int
	ProgressTimeouts              int
	TimestampWarningCount         int
	TopRepeatedBeads              []ThroughputCount
	TopRepeatedFingerprints       []ThroughputCount
	ProductivePerAssignment       float64
	QGRejectionsPerAssignment     float64
	ReviewRejectionsPerAssignment float64
	ProgressTimeoutsPerAssignment float64
	Baseline                      ThroughputBaselineComparison
}

type ThroughputCount struct {
	Key   string
	Count int
}

type ThroughputBaselineComparison struct {
	Name                         string
	ProductivePerAssignment      float64
	ProductivePerAssignmentDelta float64
}

type throughputAssertConfig struct {
	MinProductivePerAssignment       float64
	MaxQGRejectionsPerAssignment     float64
	MaxReviewRejectionsPerAssignment float64
	MaxProgressTimeoutsPerAssignment float64
}

type throughputCmdConfig struct {
	window time.Duration
	assert bool
	throughputAssertConfig
}

func newThroughputCmd() *cobra.Command {
	cfg := throughputCmdConfig{
		window: time.Hour,
		throughputAssertConfig: throughputAssertConfig{
			MaxQGRejectionsPerAssignment:     -1,
			MaxReviewRejectionsPerAssignment: -1,
			MaxProgressTimeoutsPerAssignment: -1,
		},
	}
	cmd := &cobra.Command{
		Use:   "throughput",
		Short: "Report swarm throughput health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			db, err := openStateDB(paths.StateDBPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer func() { _ = db.Close() }()
			health, err := computeThroughputHealth(cmd.Context(), db, cfg.window)
			if err != nil {
				return err
			}
			formatThroughputHealth(cmd.OutOrStdout(), health)
			if cfg.assert {
				return assertThroughputHealth(health, cfg.throughputAssertConfig)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&cfg.window, "window", time.Hour, "rolling window to report")
	cmd.Flags().BoolVar(&cfg.assert, "assert", false, "exit nonzero when throughput thresholds are missed")
	cmd.Flags().Float64Var(&cfg.MinProductivePerAssignment, "min-productive-per-assignment", 0, "minimum productive closures per assignment")
	cmd.Flags().Float64Var(&cfg.MaxQGRejectionsPerAssignment, "max-qg-rejections-per-assignment", -1, "maximum QG rejections per assignment")
	cmd.Flags().Float64Var(&cfg.MaxReviewRejectionsPerAssignment, "max-review-rejections-per-assignment", -1, "maximum review rejections per assignment")
	cmd.Flags().Float64Var(&cfg.MaxProgressTimeoutsPerAssignment, "max-progress-timeouts-per-assignment", -1, "maximum progress timeouts per assignment")
	return cmd
}

func computeThroughputHealth(ctx context.Context, db *sql.DB, window time.Duration) (ThroughputHealth, error) {
	rows, err := queryThroughputRows(ctx, db)
	if err != nil {
		return ThroughputHealth{}, err
	}
	end := latestThroughputTimestamp(rows)
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start := end.Add(-window)
	health := ThroughputHealth{
		WindowStart: start,
		WindowEnd:   end,
		Baseline: ThroughputBaselineComparison{
			Name:                    "May-11",
			ProductivePerAssignment: may11ProductivePerAssignmentBaseline,
		},
	}
	beadRepeats := make(map[string]int)
	fingerprintRepeats := make(map[string]int)
	for _, row := range rows {
		accumulateThroughputRow(&health, row, start, end, beadRepeats, fingerprintRepeats)
	}
	finalizeThroughputHealth(&health, beadRepeats, fingerprintRepeats)
	return health, nil
}

func accumulateThroughputRow(health *ThroughputHealth, row throughputRow, start, end time.Time, beadRepeats, fingerprintRepeats map[string]int) {
	if row.timestampMalformed {
		health.TimestampWarningCount++
		return
	}
	if row.ts.Before(start) || row.ts.After(end) {
		return
	}
	switch row.kind {
	case "assignment":
		health.Assignments++
	case "bead":
		accumulateThroughputBead(health, row)
	case "event":
		accumulateThroughputEvent(health, row, beadRepeats, fingerprintRepeats)
	}
}

func accumulateThroughputBead(health *ThroughputHealth, row throughputRow) {
	if isDeferredClose(row.closeReason) {
		health.DeferredClosures++
		return
	}
	health.ProductiveClosures++
}

func accumulateThroughputEvent(health *ThroughputHealth, row throughputRow, beadRepeats, fingerprintRepeats map[string]int) {
	switch row.eventType {
	case "quality_gate_rejected", "qg_failed", "pre_merge_qg_error_classified":
		health.QGRejections++
		if row.beadID != "" {
			beadRepeats[row.beadID]++
		}
		if fp := eventFingerprint(row.payload); fp != "" {
			fingerprintRepeats[fp]++
		}
	case "review_rejected", "review_failed":
		health.ReviewRejections++
	case "progress_timeout":
		health.ProgressTimeouts++
	}
}

func finalizeThroughputHealth(health *ThroughputHealth, beadRepeats, fingerprintRepeats map[string]int) {
	health.TopRepeatedBeads = topThroughputCounts(beadRepeats, 5)
	health.TopRepeatedFingerprints = topThroughputCounts(fingerprintRepeats, 5)
	health.ProductivePerAssignment = ratio(health.ProductiveClosures, health.Assignments)
	health.QGRejectionsPerAssignment = ratio(health.QGRejections, health.Assignments)
	health.ReviewRejectionsPerAssignment = ratio(health.ReviewRejections, health.Assignments)
	health.ProgressTimeoutsPerAssignment = ratio(health.ProgressTimeouts, health.Assignments)
	health.Baseline.ProductivePerAssignmentDelta = health.ProductivePerAssignment - health.Baseline.ProductivePerAssignment
}

type throughputRow struct {
	kind               string
	eventType          string
	beadID             string
	payload            string
	closeReason        string
	ts                 time.Time
	timestampMalformed bool
}

func queryThroughputRows(ctx context.Context, db *sql.DB) ([]throughputRow, error) {
	var rows []throughputRow
	assignments, err := queryThroughputAssignments(ctx, db)
	if err != nil {
		return nil, err
	}
	rows = append(rows, assignments...)
	beads, err := queryThroughputClosedBeads(ctx, db)
	if err != nil {
		return nil, err
	}
	rows = append(rows, beads...)
	events, err := queryThroughputEvents(ctx, db)
	if err != nil {
		return nil, err
	}
	rows = append(rows, events...)
	return rows, nil
}

func queryThroughputAssignments(ctx context.Context, db *sql.DB) ([]throughputRow, error) {
	var rows []throughputRow
	assignments, err := db.QueryContext(ctx, `SELECT assigned_at FROM assignments`)
	if err != nil {
		return nil, fmt.Errorf("query assignments: %w", err)
	}
	for assignments.Next() {
		var ts string
		if err := assignments.Scan(&ts); err != nil {
			_ = assignments.Close()
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		rows = append(rows, newThroughputRow("assignment", "", "", "", "", ts))
	}
	if err := assignments.Close(); err != nil {
		return nil, fmt.Errorf("close assignments rows: %w", err)
	}
	if err := assignments.Err(); err != nil {
		return nil, fmt.Errorf("iterate assignments: %w", err)
	}
	return rows, nil
}

func queryThroughputClosedBeads(ctx context.Context, db *sql.DB) ([]throughputRow, error) {
	var rows []throughputRow
	beads, err := db.QueryContext(ctx, `SELECT id, close_reason, closed_at FROM beads WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at <> ''`)
	if err != nil {
		return nil, fmt.Errorf("query closed beads: %w", err)
	}
	for beads.Next() {
		var id, closedAt string
		var closeReason sql.NullString
		if err := beads.Scan(&id, &closeReason, &closedAt); err != nil {
			_ = beads.Close()
			return nil, fmt.Errorf("scan closed bead: %w", err)
		}
		rows = append(rows, newThroughputRow("bead", "", id, "", closeReason.String, closedAt))
	}
	if err := beads.Close(); err != nil {
		return nil, fmt.Errorf("close beads rows: %w", err)
	}
	if err := beads.Err(); err != nil {
		return nil, fmt.Errorf("iterate closed beads: %w", err)
	}
	return rows, nil
}

func queryThroughputEvents(ctx context.Context, db *sql.DB) ([]throughputRow, error) {
	var rows []throughputRow
	events, err := db.QueryContext(ctx, `SELECT type, bead_id, payload, created_at FROM events`)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	for events.Next() {
		var eventType, createdAt string
		var beadID, payload sql.NullString
		if err := events.Scan(&eventType, &beadID, &payload, &createdAt); err != nil {
			_ = events.Close()
			return nil, fmt.Errorf("scan event: %w", err)
		}
		rows = append(rows, newThroughputRow("event", eventType, beadID.String, payload.String, "", createdAt))
	}
	if err := events.Close(); err != nil {
		return nil, fmt.Errorf("close events rows: %w", err)
	}
	if err := events.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return rows, nil
}

func newThroughputRow(kind, eventType, beadID, payload, closeReason, rawTS string) throughputRow {
	ts, err := parseThroughputTimestamp(rawTS)
	return throughputRow{
		kind:               kind,
		eventType:          eventType,
		beadID:             beadID,
		payload:            payload,
		closeReason:        closeReason,
		ts:                 ts,
		timestampMalformed: err != nil,
	}
}

func parseThroughputTimestamp(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q", raw)
}

func latestThroughputTimestamp(rows []throughputRow) time.Time {
	var latest time.Time
	for _, row := range rows {
		if row.timestampMalformed {
			continue
		}
		if row.ts.After(latest) {
			latest = row.ts
		}
	}
	return latest
}

func isDeferredClose(reason string) bool {
	reason = strings.ToLower(reason)
	return strings.Contains(reason, "defer") || strings.Contains(reason, "duplicate")
}

func eventFingerprint(payload string) string {
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return ""
	}
	if fp, ok := fields["fingerprint"].(string); ok {
		return fp
	}
	return ""
}

func topThroughputCounts(counts map[string]int, limit int) []ThroughputCount {
	top := make([]ThroughputCount, 0, len(counts))
	for key, count := range counts {
		if count > 1 {
			top = append(top, ThroughputCount{Key: key, Count: count})
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count == top[j].Count {
			return top[i].Key < top[j].Key
		}
		return top[i].Count > top[j].Count
	})
	if len(top) > limit {
		top = top[:limit]
	}
	return top
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func assertThroughputHealth(health ThroughputHealth, cfg throughputAssertConfig) error {
	var misses []string
	if cfg.MinProductivePerAssignment > 0 && health.ProductivePerAssignment < cfg.MinProductivePerAssignment {
		misses = append(misses, fmt.Sprintf("productive_per_assignment %.3f < %.3f", health.ProductivePerAssignment, cfg.MinProductivePerAssignment))
	}
	if cfg.MaxQGRejectionsPerAssignment >= 0 && health.QGRejectionsPerAssignment > cfg.MaxQGRejectionsPerAssignment {
		misses = append(misses, fmt.Sprintf("qg_rejections_per_assignment %.3f > %.3f", health.QGRejectionsPerAssignment, cfg.MaxQGRejectionsPerAssignment))
	}
	if cfg.MaxReviewRejectionsPerAssignment >= 0 && health.ReviewRejectionsPerAssignment > cfg.MaxReviewRejectionsPerAssignment {
		misses = append(misses, fmt.Sprintf("review_rejections_per_assignment %.3f > %.3f", health.ReviewRejectionsPerAssignment, cfg.MaxReviewRejectionsPerAssignment))
	}
	if cfg.MaxProgressTimeoutsPerAssignment >= 0 && health.ProgressTimeoutsPerAssignment > cfg.MaxProgressTimeoutsPerAssignment {
		misses = append(misses, fmt.Sprintf("progress_timeouts_per_assignment %.3f > %.3f", health.ProgressTimeoutsPerAssignment, cfg.MaxProgressTimeoutsPerAssignment))
	}
	if len(misses) > 0 {
		return fmt.Errorf("throughput health thresholds missed: %s", strings.Join(misses, "; "))
	}
	return nil
}

func formatThroughputHealth(w io.Writer, health ThroughputHealth) {
	fmt.Fprintf(w, "throughput health (%s to %s)\n", health.WindowStart.Format(time.RFC3339), health.WindowEnd.Format(time.RFC3339))
	fmt.Fprintf(w, "  assignments: %d\n", health.Assignments)
	fmt.Fprintf(w, "  closures: productive=%d deferred=%d\n", health.ProductiveClosures, health.DeferredClosures)
	fmt.Fprintf(w, "  rejections: qg=%d review=%d progress_timeouts=%d\n", health.QGRejections, health.ReviewRejections, health.ProgressTimeouts)
	fmt.Fprintf(w, "  ratios: productive_per_assignment=%.3f qg_rejections_per_assignment=%.3f review_rejections_per_assignment=%.3f progress_timeouts_per_assignment=%.3f\n",
		health.ProductivePerAssignment, health.QGRejectionsPerAssignment, health.ReviewRejectionsPerAssignment, health.ProgressTimeoutsPerAssignment)
	fmt.Fprintf(w, "  timestamp warnings: %d\n", health.TimestampWarningCount)
	fmt.Fprintf(w, "  baseline %s: productive_per_assignment_delta=%.3f\n", health.Baseline.Name, health.Baseline.ProductivePerAssignmentDelta)
	formatThroughputCounts(w, "  repeated beads", health.TopRepeatedBeads)
	formatThroughputCounts(w, "  repeated fingerprints", health.TopRepeatedFingerprints)
}

func formatThroughputCounts(w io.Writer, label string, counts []ThroughputCount) {
	if len(counts) == 0 {
		fmt.Fprintf(w, "%s: none\n", label)
		return
	}
	fmt.Fprintf(w, "%s:\n", label)
	for _, count := range counts {
		fmt.Fprintf(w, "    %s %d\n", count.Key, count.Count)
	}
}
