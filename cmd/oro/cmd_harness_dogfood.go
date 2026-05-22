package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

const (
	dogfoodScenarioDefault       = "default"
	dogfoodScenarioReliabilityV2 = "reliability-v2"

	dogfoodNoopMergeID     = "oro-dogfood-noop-merge"
	dogfoodTargetCleanupID = "oro-dogfood-target-cleanup"
	dogfoodOpsVisibilityID = "oro-dogfood-ops-visibility"

	dogfoodTargetBranch = "dogfood-target"

	dogfoodReliabilityV2OpsVisibilityEvent = "dogfood_reliability_v2_ops_visibility"
)

type dogfoodHarnessConfig struct {
	scenario   string
	iterations int
	workers    int
	interval   time.Duration
}

type dogfoodSeededWork struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria string
	TargetBranch       string
}

func newHarnessDogfoodCmd() *cobra.Command {
	return newHarnessDogfoodCmdWithRunner(&cliMonitorRunner{})
}

func newHarnessDogfoodCmdWithRunner(runner monitorRunner) *cobra.Command {
	cfg := dogfoodHarnessConfig{
		scenario:   dogfoodScenarioDefault,
		iterations: 3,
		workers:    2,
		interval:   time.Second,
	}
	cmd := &cobra.Command{
		Use:          "dogfood",
		Short:        "Seed, run, and assert finite monitor dogfood scenarios",
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newHarnessDogfoodSeedCmd(&cfg),
		newHarnessDogfoodRunCmd(&cfg, runner),
		newHarnessDogfoodAssertCmd(&cfg),
	)
	return cmd
}

func newHarnessDogfoodSeedCmd(cfg *dogfoodHarnessConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed finite monitor dogfood work into the isolated state DB",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return seedDogfoodHarness(cmd.Context(), cmd.OutOrStdout(), *cfg)
		},
	}
	addDogfoodScenarioFlag(cmd, cfg)
	return cmd
}

func newHarnessDogfoodRunCmd(cfg *dogfoodHarnessConfig, runner monitorRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run monitor --act for a finite dogfood interval",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDogfoodHarness(cmd.Context(), cmd.OutOrStdout(), *cfg, runner)
		},
	}
	addDogfoodScenarioFlag(cmd, cfg)
	cmd.Flags().IntVar(&cfg.iterations, "iterations", cfg.iterations, "finite monitor iterations to run")
	cmd.Flags().IntVar(&cfg.workers, "workers", cfg.workers, "target and max worker count")
	cmd.Flags().DurationVar(&cfg.interval, "interval", cfg.interval, "monitor interval")
	return cmd
}

func newHarnessDogfoodAssertCmd(cfg *dogfoodHarnessConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assert",
		Short: "Assert dogfood state invariants after the factory stops",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return assertDogfoodHarness(cmd.Context(), cmd.OutOrStdout(), *cfg)
		},
	}
	addDogfoodScenarioFlag(cmd, cfg)
	return cmd
}

func addDogfoodScenarioFlag(cmd *cobra.Command, cfg *dogfoodHarnessConfig) {
	cmd.Flags().StringVar(&cfg.scenario, "scenario", cfg.scenario, "dogfood scenario: default or reliability-v2")
}

func seedDogfoodHarness(ctx context.Context, w io.Writer, cfg dogfoodHarnessConfig) error {
	cfg = normalizeDogfoodHarnessConfig(cfg)
	if err := validateDogfoodScenario(cfg.scenario); err != nil {
		return err
	}
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve dogfood state paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return fmt.Errorf("open dogfood state db: %w", err)
	}
	defer db.Close()

	store := beadstore.NewSQLiteStore(db)
	work := dogfoodScenarioWork(cfg.scenario)
	for _, item := range work {
		if err := ensureDogfoodWork(ctx, db, store, item); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "seeded dogfood scenario %s\n", cfg.scenario)
	fmt.Fprintf(w, "state_db=%s\n", paths.StateDBPath)
	for _, item := range work {
		fmt.Fprintf(w, "seeded=%s\n", item.ID)
	}
	return nil
}

func runDogfoodHarness(ctx context.Context, w io.Writer, cfg dogfoodHarnessConfig, runner monitorRunner) error {
	cfg = normalizeDogfoodHarnessConfig(cfg)
	if err := validateDogfoodScenario(cfg.scenario); err != nil {
		return err
	}
	if cfg.scenario == dogfoodScenarioReliabilityV2 {
		if err := exerciseReliabilityV2OpsVisibility(ctx, w, cfg, runner); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "monitor --act iterations=%d workers=%d interval=%s\n", cfg.iterations, cfg.workers, cfg.interval)
	return runMonitor(ctx, w, monitorConfig{
		targetWorkers: cfg.workers,
		maxWorkers:    cfg.workers,
		interval:      cfg.interval,
		act:           true,
		iterations:    cfg.iterations,
		restartAfter:  1,
	}, runner, newMonitorState())
}

func assertDogfoodHarness(ctx context.Context, w io.Writer, cfg dogfoodHarnessConfig) error {
	cfg = normalizeDogfoodHarnessConfig(cfg)
	if err := validateDogfoodScenario(cfg.scenario); err != nil {
		return err
	}
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve dogfood state paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return fmt.Errorf("open dogfood state db: %w", err)
	}
	defer db.Close()

	failures := dogfoodInvariantFailures(ctx, db, cfg.scenario)
	if len(failures) > 0 {
		fmt.Fprintln(w, "dogfood invariants FAIL")
		fmt.Fprintf(w, "state_db=%s\n", paths.StateDBPath)
		for _, failure := range failures {
			fmt.Fprintf(w, "- %s\n", failure)
		}
		return fmt.Errorf("dogfood invariants failed: %s", strings.Join(failures, "; "))
	}
	fmt.Fprintln(w, "dogfood invariants PASS")
	fmt.Fprintf(w, "state_db=%s\n", paths.StateDBPath)
	if cfg.scenario == dogfoodScenarioReliabilityV2 {
		fmt.Fprintln(w, "target-aware cleanup evidence: present")
		fmt.Fprintln(w, "no-op merge closure evidence: present")
		fmt.Fprintln(w, "ops failure visibility evidence: present")
	}
	return nil
}

func normalizeDogfoodHarnessConfig(cfg dogfoodHarnessConfig) dogfoodHarnessConfig {
	cfg.scenario = strings.TrimSpace(cfg.scenario)
	if cfg.scenario == "" {
		cfg.scenario = dogfoodScenarioDefault
	}
	if cfg.iterations <= 0 {
		cfg.iterations = 3
	}
	if cfg.workers <= 0 {
		cfg.workers = 2
	}
	if cfg.interval <= 0 {
		cfg.interval = time.Second
	}
	return cfg
}

func validateDogfoodScenario(scenario string) error {
	switch scenario {
	case dogfoodScenarioDefault, dogfoodScenarioReliabilityV2:
		return nil
	default:
		return fmt.Errorf("unknown dogfood scenario %q", scenario)
	}
}

func dogfoodScenarioWork(scenario string) []dogfoodSeededWork {
	if scenario == dogfoodScenarioReliabilityV2 {
		return []dogfoodSeededWork{
			{
				ID:                 dogfoodTargetCleanupID,
				Title:              "Dogfood target-aware cleanup",
				Description:        "Deterministic dogfood work assigned from a non-default target branch to prove no-op cleanup uses the target branch.",
				AcceptanceCriteria: "Test: finite monitor dogfood | Cmd: true | Assert: no-op merge cleanup records the non-default target branch",
				TargetBranch:       dogfoodTargetBranch,
			},
		}
	}

	return []dogfoodSeededWork{
		{
			ID:                 dogfoodNoopMergeID,
			Title:              "Dogfood no-op merge closure",
			Description:        "Deterministic dogfood work that intentionally makes no source changes, proving no-op merge closure.",
			AcceptanceCriteria: "Test: finite monitor dogfood | Cmd: true | Assert: dispatcher closes a no-op merge without reopening work",
		},
	}
}

func ensureDogfoodWork(ctx context.Context, db *sql.DB, store beadstore.Store, item dogfoodSeededWork) error {
	if bead, err := store.Show(ctx, item.ID); err == nil {
		if bead != nil {
			return ensureDogfoodMetadata(ctx, db, item)
		}
	} else {
		var notFound *protocol.BeadNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("show dogfood work %s: %w", item.ID, err)
		}
	}
	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:                 item.ID,
		Title:              item.Title,
		Description:        item.Description,
		AcceptanceCriteria: item.AcceptanceCriteria,
		Priority:           0,
		Type:               "task",
		Tier:               string(protocol.TierFast),
	}); err != nil {
		return fmt.Errorf("create dogfood work %s: %w", item.ID, err)
	}
	return ensureDogfoodMetadata(ctx, db, item)
}

func ensureDogfoodMetadata(ctx context.Context, db *sql.DB, item dogfoodSeededWork) error {
	if item.TargetBranch == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO bead_metadata (bead_id, key, value)
VALUES (?, ?, ?)
ON CONFLICT(bead_id, key) DO UPDATE SET value=excluded.value`,
		item.ID, "branch", item.TargetBranch); err != nil {
		return fmt.Errorf("write target branch metadata for %s: %w", item.ID, err)
	}
	return nil
}

func exerciseReliabilityV2OpsVisibility(ctx context.Context, w io.Writer, cfg dogfoodHarnessConfig, runner monitorRunner) error {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve dogfood state paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return fmt.Errorf("open dogfood state db: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
UPDATE ops_runs
   SET status='completed', completed_at=datetime('now')
 WHERE type='dogfood-reliability-v2'
   AND bead_id=?`, dogfoodOpsVisibilityID); err != nil {
		return fmt.Errorf("clear prior reliability-v2 ops run: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO ops_runs (type, bead_id, status, error, started_at)
VALUES ('dogfood-reliability-v2', ?, 'failed', 'dogfood reliability-v2 visibility probe', datetime('now'))`,
		dogfoodOpsVisibilityID); err != nil {
		return fmt.Errorf("seed reliability-v2 failed ops run: %w", err)
	}

	var probe bytes.Buffer
	if err := runMonitorIteration(ctx, &probe, monitorConfig{
		targetWorkers: cfg.workers,
		maxWorkers:    cfg.workers,
		interval:      cfg.interval,
		act:           true,
		restartAfter:  1,
	}, runner, newMonitorState()); err != nil {
		return fmt.Errorf("exercise reliability-v2 ops visibility: %w", err)
	}
	if !strings.Contains(probe.String(), "blocked_by_ops_runs") && !strings.Contains(probe.String(), "ops_run_failed") {
		return fmt.Errorf("reliability-v2 ops visibility probe did not block on failed ops run:\n%s", probe.String())
	}
	fmt.Fprint(w, probe.String())

	if _, err := db.ExecContext(ctx, `
UPDATE ops_runs
   SET status='completed', completed_at=datetime('now')
 WHERE type='dogfood-reliability-v2'
   AND bead_id=?`, dogfoodOpsVisibilityID); err != nil {
		return fmt.Errorf("resolve reliability-v2 ops run: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO events (type, source, bead_id, payload)
VALUES (?, 'dogfood', ?, '{"blocked_by_ops_runs":true}')`,
		dogfoodReliabilityV2OpsVisibilityEvent, dogfoodOpsVisibilityID); err != nil {
		return fmt.Errorf("record reliability-v2 ops visibility evidence: %w", err)
	}
	return nil
}

func dogfoodInvariantFailures(ctx context.Context, db *sql.DB, scenario string) []string {
	var failures []string
	failures = append(failures, dogfoodCountFailure(ctx, db, "active assignments", `
SELECT COUNT(*) FROM assignments WHERE status='active'`)...)
	failures = append(failures, dogfoodCountFailure(ctx, db, "recovery quarantines", `
SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`)...)
	failures = append(failures, dogfoodCountFailure(ctx, db, "QG incidents", `
SELECT COUNT(*) FROM qg_failure_incidents WHERE status='open'`)...)
	failures = append(failures, dogfoodCountFailure(ctx, db, "failed/stale ops runs", `
SELECT COUNT(*) FROM ops_runs WHERE status IN ('failed', 'stale')`)...)
	failures = append(failures, dogfoodReadyWorkFailures(ctx, db, scenario)...)
	if scenario == dogfoodScenarioReliabilityV2 {
		failures = append(failures, dogfoodReliabilityV2EvidenceFailures(ctx, db)...)
	}
	return failures
}

func dogfoodCountFailure(ctx context.Context, db *sql.DB, label, query string) []string {
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return []string{fmt.Sprintf("%s query failed: %v", label, err)}
	}
	if count == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s remain: %d", label, count)}
}

func dogfoodReadyWorkFailures(ctx context.Context, db *sql.DB, scenario string) []string {
	ids := dogfoodScenarioIDs(scenario)
	var openIDs []string
	for _, id := range ids {
		var status string
		err := db.QueryRowContext(ctx, `
SELECT status
  FROM beads
 WHERE deleted=0
   AND id=?`, id).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			openIDs = append(openIDs, id)
			continue
		}
		if err != nil {
			return []string{fmt.Sprintf("ready seeded work query failed for %s: %v", id, err)}
		}
		if status != "closed" {
			openIDs = append(openIDs, id)
		}
	}
	if len(openIDs) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("ready seeded work remain: %s", strings.Join(openIDs, ","))}
}

func dogfoodReliabilityV2EvidenceFailures(ctx context.Context, db *sql.DB) []string {
	var failures []string
	if !dogfoodEventExists(ctx, db, "merge_noop", dogfoodTargetCleanupID, dogfoodTargetBranch) {
		failures = append(failures, "missing reliability-v2 target-aware cleanup evidence")
	}
	if !dogfoodEventExists(ctx, db, "merge_noop", dogfoodTargetCleanupID, "") {
		failures = append(failures, "missing reliability-v2 no-op merge closure evidence")
	}
	if !dogfoodEventExists(ctx, db, dogfoodReliabilityV2OpsVisibilityEvent, dogfoodOpsVisibilityID, "") {
		failures = append(failures, "missing reliability-v2 ops failure visibility evidence")
	}
	return failures
}

func dogfoodEventExists(ctx context.Context, db *sql.DB, eventType, beadID, payloadContains string) bool {
	query := `SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`
	args := []any{eventType, beadID}
	if payloadContains != "" {
		query += ` AND payload LIKE ?`
		args = append(args, "%"+payloadContains+"%")
	}
	var count int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func dogfoodScenarioIDs(scenario string) []string {
	work := dogfoodScenarioWork(scenario)
	ids := make([]string, 0, len(work))
	for _, item := range work {
		ids = append(ids, item.ID)
	}
	return ids
}
