package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"oro/pkg/factoryhealth"

	"github.com/spf13/cobra"
)

type monitorConfig struct {
	targetWorkers int
	maxWorkers    int
	interval      time.Duration
	act           bool
	failOnUnsafe  bool
	restartAfter  int
	iterations    int
}

type monitorRunner interface {
	FactoryHealth(context.Context) (factoryhealth.FactoryHealth, error)
	Resume(context.Context) error
	Pause(context.Context) error
	Scale(context.Context, int) error
	MaxWorkers(context.Context, int) error
	RestartDaemon(context.Context, int, int) error
	RecentMonitorAction(context.Context, string, string, time.Duration) (bool, error)
	RecordMonitorAction(context.Context, monitorAction) error
}

const (
	monitorActionDedupeWindow          = 2 * time.Hour
	monitorActionQGChurnPause          = "qg_churn_pause"
	monitorActionDaemonRestart         = "daemon_restart"
	monitorActionScaleWorkers          = "scale_workers"
	monitorActionMaxWorkers            = "max_workers"
	monitorActionRecoveryQuarantineLog = "recovery_quarantine_block"
)

type monitorAction struct {
	Action  string
	Key     string
	Payload string
}

type monitorState struct {
	repeatedFindings         map[string]int
	restartIssued            bool
	lastRecoveryBlockedCount int
	qgChurnFingerprint       string
	qgChurnLastOccurrences   int
	qgChurnIncreaseStreak    int
	qgChurnPausedFingerprint string
}

func newMonitorState() *monitorState {
	return &monitorState{repeatedFindings: make(map[string]int), lastRecoveryBlockedCount: -1}
}

func newMonitorCmd() *cobra.Command {
	cfg := monitorConfig{
		interval:     time.Minute,
		restartAfter: 2,
	}
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Observe factory health and optionally perform bounded recovery",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonitor(cmd.Context(), cmd.OutOrStdout(), cfg, &cliMonitorRunner{}, newMonitorState())
		},
	}
	cmd.Flags().IntVar(&cfg.targetWorkers, "target", 0, "target worker count to maintain")
	cmd.Flags().IntVar(&cfg.maxWorkers, "max-workers", 0, "maximum worker count to maintain")
	cmd.Flags().DurationVar(&cfg.interval, "interval", time.Minute, "health check interval")
	cmd.Flags().BoolVar(&cfg.act, "act", false, "perform bounded recovery actions")
	cmd.Flags().BoolVar(&cfg.failOnUnsafe, "fail-on-unsafe", false, "exit nonzero when factory health is unsafe")
	cmd.Flags().IntVar(&cfg.iterations, "iterations", 0, "number of monitor iterations before exiting (0 means forever)")
	_ = cmd.Flags().MarkHidden("iterations")
	return cmd
}

func runMonitor(ctx context.Context, w io.Writer, cfg monitorConfig, runner monitorRunner, state *monitorState) error {
	if cfg.interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if cfg.restartAfter <= 0 {
		cfg.restartAfter = 2
	}
	if cfg.iterations == 0 {
		for {
			if err := runMonitorIteration(ctx, w, cfg, runner, state); err != nil {
				return err
			}
			timer := time.NewTimer(cfg.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("monitor canceled: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
	for i := 0; i < cfg.iterations; i++ {
		if err := runMonitorIteration(ctx, w, cfg, runner, state); err != nil {
			return err
		}
		if i == cfg.iterations-1 {
			break
		}
		timer := time.NewTimer(cfg.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("monitor canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return nil
}

func runMonitorIteration(ctx context.Context, w io.Writer, cfg monitorConfig, runner monitorRunner, state *monitorState) error {
	health, err := runner.FactoryHealth(ctx)
	if err != nil {
		return fmt.Errorf("query factory health: %w", err)
	}
	fmt.Fprintf(w, "health=%s posture=%q findings=%d\n", health.State, health.Posture, len(health.Findings))
	for _, finding := range health.Findings {
		fmt.Fprintf(w, "  %s [%s] %s", finding.Code, finding.Severity, finding.Message)
		if finding.RecommendedAction != "" {
			fmt.Fprintf(w, " | action: %s", finding.RecommendedAction)
		}
		fmt.Fprintln(w)
	}
	updateRepeatedFindings(state, health)
	if cfg.failOnUnsafe && health.State == factoryhealth.StateUnsafe {
		return fmt.Errorf("factory health unsafe: %s", health.Posture)
	}
	if !cfg.act {
		return nil
	}
	if hasRecoveryQuarantineFinding(health) {
		return writeRecoveryQuarantineBlocked(ctx, w, runner, state, health)
	}
	if health.State == factoryhealth.StateStopped || !health.Metrics.DaemonRunning {
		fmt.Fprintf(w, "blocked_by_daemon_stopped action=%q\n", "run oro start after confirming no unsafe assignments remain")
		return nil
	}
	if health.State == factoryhealth.StateUnsafe {
		fmt.Fprintf(w, "blocked_by_unsafe_health action=%q\n", "inspect oro health findings and resolve critical state before monitor mutates workers")
		return nil
	}
	if hasBlockingOpsRuns(health) {
		writeOpsRunsBlocked(w, health)
		return nil
	}
	return actOnMonitorHealth(ctx, w, cfg, runner, state, health)
}

func updateRepeatedFindings(state *monitorState, health factoryhealth.FactoryHealth) {
	current := make(map[string]bool, len(health.Findings))
	for _, finding := range health.Findings {
		current[finding.Code] = true
		state.repeatedFindings[finding.Code]++
	}
	for code := range state.repeatedFindings {
		if !current[code] {
			delete(state.repeatedFindings, code)
		}
	}
}

func actOnMonitorHealth(ctx context.Context, w io.Writer, cfg monitorConfig, runner monitorRunner, state *monitorState, health factoryhealth.FactoryHealth) error {
	if hasRecoveryQuarantineFinding(health) {
		return nil
	}
	handled, err := actOnQGChurn(ctx, w, cfg, runner, state, health)
	if handled || err != nil {
		return err
	}
	if err := maintainMonitorWorkerTargets(ctx, cfg, runner, health.Metrics); err != nil {
		return err
	}
	if err := resumePausedMonitor(ctx, runner, health.Metrics, health.Findings); err != nil {
		return err
	}
	return restartMonitorIfNeeded(ctx, cfg, runner, state, health)
}

func maintainMonitorWorkerTargets(ctx context.Context, cfg monitorConfig, runner monitorRunner, metrics factoryhealth.Metrics) error {
	if cfg.maxWorkers > 0 && metrics.MaxWorkers > 0 && metrics.MaxWorkers != cfg.maxWorkers {
		action := monitorAction{
			Action:  monitorActionMaxWorkers,
			Key:     fmt.Sprintf("%d:%d", metrics.MaxWorkers, cfg.maxWorkers),
			Payload: fmt.Sprintf(`{"from":%d,"to":%d}`, metrics.MaxWorkers, cfg.maxWorkers),
		}
		if _, err := performMonitorAction(ctx, runner, action, func() error {
			return runner.MaxWorkers(ctx, cfg.maxWorkers)
		}); err != nil {
			return fmt.Errorf("set max workers: %w", err)
		}
	}
	if cfg.targetWorkers > 0 && shouldScaleMonitor(cfg, metrics) {
		action := monitorAction{
			Action:  monitorActionScaleWorkers,
			Key:     fmt.Sprintf("%d:%d:%d", metrics.WorkerCount, metrics.TargetWorkers, cfg.targetWorkers),
			Payload: fmt.Sprintf(`{"workers":%d,"target_workers":%d,"requested":%d}`, metrics.WorkerCount, metrics.TargetWorkers, cfg.targetWorkers),
		}
		if _, err := performMonitorAction(ctx, runner, action, func() error {
			return runner.Scale(ctx, cfg.targetWorkers)
		}); err != nil {
			return fmt.Errorf("scale workers: %w", err)
		}
	}
	return nil
}

func resumePausedMonitor(ctx context.Context, runner monitorRunner, metrics factoryhealth.Metrics, findings []factoryhealth.Finding) error {
	if metrics.PauseSource != "monitor" {
		return nil
	}
	for _, finding := range findings {
		if finding.Code == factoryhealth.FindingPausedWithReadyQueue {
			if err := runner.Resume(ctx); err != nil {
				return fmt.Errorf("resume dispatcher: %w", err)
			}
		}
	}
	return nil
}

func restartMonitorIfNeeded(ctx context.Context, cfg monitorConfig, runner monitorRunner, state *monitorState, health factoryhealth.FactoryHealth) error {
	code, ok := restartMonitorFindingCode(cfg, state, health)
	if !ok {
		return nil
	}
	workers := monitorRestartWorkers(cfg, health.Metrics)
	maxWorkers := monitorRestartMaxWorkers(cfg, health.Metrics, workers)
	action := monitorAction{
		Action:  monitorActionDaemonRestart,
		Key:     fmt.Sprintf("%s:%d:%d", code, workers, maxWorkers),
		Payload: fmt.Sprintf(`{"finding":%q,"workers":%d,"max_workers":%d}`, code, workers, maxWorkers),
	}
	performed, err := performMonitorAction(ctx, runner, action, func() error {
		return runner.RestartDaemon(ctx, workers, maxWorkers)
	})
	if err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}
	if performed {
		state.restartIssued = true
	}
	return nil
}

func actOnQGChurn(ctx context.Context, w io.Writer, cfg monitorConfig, runner monitorRunner, state *monitorState, health factoryhealth.FactoryHealth) (bool, error) {
	if fingerprint, ok := qgIncidentIncreaseFingerprint(health); ok && fingerprint != "" {
		recent, err := runner.RecentMonitorAction(ctx, monitorActionQGChurnPause, fingerprint, monitorActionDedupeWindow)
		if err != nil {
			return false, fmt.Errorf("check qg churn action ledger: %w", err)
		}
		if recent {
			state.qgChurnFingerprint = fingerprint
			state.qgChurnPausedFingerprint = fingerprint
			writeQGChurnBlocked(w, state, health)
			return true, nil
		}
	}
	if shouldPauseForQGChurn(cfg, state, health) {
		state.qgChurnPausedFingerprint = state.qgChurnFingerprint
		if err := runner.Pause(ctx); err != nil {
			return true, fmt.Errorf("pause dispatcher for qg churn: %w", err)
		}
		if err := runner.RecordMonitorAction(ctx, monitorAction{
			Action:  monitorActionQGChurnPause,
			Key:     state.qgChurnFingerprint,
			Payload: fmt.Sprintf(`{"fingerprint":%q,"occurrences_30m":%d}`, state.qgChurnFingerprint, health.Metrics.QGOccurrences30m),
		}); err != nil {
			return true, fmt.Errorf("record qg churn pause: %w", err)
		}
		writeQGChurnBlocked(w, state, health)
		return true, nil
	}
	if qgChurnMutationBlocked(state, health) {
		writeQGChurnBlocked(w, state, health)
		return true, nil
	}
	return false, nil
}

func shouldPauseForQGChurn(cfg monitorConfig, state *monitorState, health factoryhealth.FactoryHealth) bool {
	threshold := cfg.restartAfter
	if threshold <= 0 {
		threshold = 2
	}
	fingerprint, streak, increasing := trackQGChurn(state, health)
	return increasing && streak >= threshold && fingerprint != state.qgChurnPausedFingerprint
}

func qgChurnMutationBlocked(state *monitorState, health factoryhealth.FactoryHealth) bool {
	if state.qgChurnPausedFingerprint != "" && hasQGIncidentIncreaseFinding(health) {
		trackQGChurn(state, health)
		return state.qgChurnFingerprint == state.qgChurnPausedFingerprint
	}
	return false
}

func trackQGChurn(state *monitorState, health factoryhealth.FactoryHealth) (fingerprint string, streak int, increasing bool) {
	fingerprint, ok := qgIncidentIncreaseFingerprint(health)
	occurrences := health.Metrics.QGOccurrences30m
	if !ok || fingerprint == "" || occurrences <= 0 {
		state.qgChurnFingerprint = ""
		state.qgChurnLastOccurrences = 0
		state.qgChurnIncreaseStreak = 0
		state.qgChurnPausedFingerprint = ""
		return "", 0, false
	}
	if state.qgChurnFingerprint != fingerprint {
		state.qgChurnFingerprint = fingerprint
		state.qgChurnLastOccurrences = occurrences
		state.qgChurnIncreaseStreak = 0
		return fingerprint, 0, false
	}
	increasing = occurrences > state.qgChurnLastOccurrences
	if increasing {
		state.qgChurnIncreaseStreak++
	} else {
		state.qgChurnIncreaseStreak = 0
	}
	state.qgChurnLastOccurrences = occurrences
	return fingerprint, state.qgChurnIncreaseStreak, increasing
}

func qgIncidentIncreaseFingerprint(health factoryhealth.FactoryHealth) (string, bool) {
	for _, finding := range health.Findings {
		if finding.Code == factoryhealth.FindingQGIncidentIncrease {
			return finding.Fingerprint, true
		}
	}
	return "", false
}

func hasQGIncidentIncreaseFinding(health factoryhealth.FactoryHealth) bool {
	_, ok := qgIncidentIncreaseFingerprint(health)
	return ok
}

func writeRecoveryQuarantineBlocked(ctx context.Context, w io.Writer, runner monitorRunner, state *monitorState, health factoryhealth.FactoryHealth) error {
	count := health.Metrics.OpenRecoveryQuarantines
	action := "run oro recovery list and oro recovery inspect <id>; resolve only after preserving or merging work"
	for _, finding := range health.Findings {
		if finding.Code != factoryhealth.FindingRecoveryQuarantineOpen {
			continue
		}
		if count == 0 {
			count = 1
		}
		if finding.RecommendedAction != "" {
			action = finding.RecommendedAction
		}
		break
	}
	if state.lastRecoveryBlockedCount == count {
		return nil
	}
	key := strconv.Itoa(count)
	recent, err := runner.RecentMonitorAction(ctx, monitorActionRecoveryQuarantineLog, key, monitorActionDedupeWindow)
	if err != nil {
		return fmt.Errorf("check recovery quarantine action ledger: %w", err)
	}
	if recent {
		state.lastRecoveryBlockedCount = count
		return nil
	}
	state.lastRecoveryBlockedCount = count
	fmt.Fprintf(w, "blocked_by_recovery_quarantine count=%d action=%q\n", count, action)
	if err := runner.RecordMonitorAction(ctx, monitorAction{
		Action:  monitorActionRecoveryQuarantineLog,
		Key:     key,
		Payload: fmt.Sprintf(`{"count":%d}`, count),
	}); err != nil {
		return fmt.Errorf("record recovery quarantine block: %w", err)
	}
	return nil
}

func writeQGChurnBlocked(w io.Writer, state *monitorState, health factoryhealth.FactoryHealth) {
	if w == nil {
		return
	}
	fingerprint := state.qgChurnFingerprint
	if fingerprint == "" {
		fingerprint, _ = qgIncidentIncreaseFingerprint(health)
	}
	occurrences := health.Metrics.QGOccurrences30m
	action := "inspect QG incident, fix or close it, then oro resume"
	fmt.Fprintf(w, "blocked_by_qg_churn fingerprint=%q occurrences_30m=%d action=%q\n", fingerprint, occurrences, action)
}

func writeOpsRunsBlocked(w io.Writer, health factoryhealth.FactoryHealth) {
	if w == nil {
		return
	}
	action := blockingOpsRunAction(health)
	fmt.Fprintf(w, "blocked_by_ops_runs failed=%d stale=%d action=%q\n", health.Metrics.OpsRuns.Failed, health.Metrics.OpsRuns.Stale, action)
}

func blockingOpsRunAction(health factoryhealth.FactoryHealth) string {
	for _, run := range health.Metrics.OpsRuns.Runs {
		if run.Status == "failed" || run.Status == "stale" {
			return factoryhealth.OpsRunRecommendedAction(run)
		}
	}
	for _, finding := range health.Findings {
		if (finding.Code == factoryhealth.FindingOpsRunFailed || finding.Code == factoryhealth.FindingOpsRunStale) && finding.RecommendedAction != "" {
			return finding.RecommendedAction
		}
	}
	return "run oro ops list, then use oro ops retry <id> or oro ops resolve <id> <reason>"
}

func hasRecoveryQuarantineFinding(health factoryhealth.FactoryHealth) bool {
	if health.Metrics.OpenRecoveryQuarantines > 0 {
		return true
	}
	for _, finding := range health.Findings {
		if finding.Code == factoryhealth.FindingRecoveryQuarantineOpen {
			return true
		}
	}
	return false
}

func hasBlockingOpsRuns(health factoryhealth.FactoryHealth) bool {
	return health.Metrics.OpsRuns.Failed > 0 || health.Metrics.OpsRuns.Stale > 0
}

func shouldScaleMonitor(cfg monitorConfig, metrics factoryhealth.Metrics) bool {
	if metrics.TargetWorkers > 0 && metrics.TargetWorkers != cfg.targetWorkers {
		return true
	}
	return metrics.WorkerCount < cfg.targetWorkers
}

func restartMonitorFindingCode(cfg monitorConfig, state *monitorState, health factoryhealth.FactoryHealth) (string, bool) {
	if state.restartIssued {
		return "", false
	}
	for _, finding := range health.Findings {
		if finding.Code == factoryhealth.FindingThroughputStall && health.Metrics.ActiveWorkers > 0 {
			continue
		}
		if restartableMonitorFinding(finding.Code) && state.repeatedFindings[finding.Code] >= cfg.restartAfter {
			return finding.Code, true
		}
		if health.State == factoryhealth.StateUnsafe && state.repeatedFindings[finding.Code] >= cfg.restartAfter {
			return finding.Code, true
		}
	}
	return "", false
}

func performMonitorAction(ctx context.Context, runner monitorRunner, action monitorAction, fn func() error) (bool, error) {
	recent, err := runner.RecentMonitorAction(ctx, action.Action, action.Key, monitorActionDedupeWindow)
	if err != nil {
		return false, fmt.Errorf("check monitor action ledger for %s: %w", action.Action, err)
	}
	if recent {
		return false, nil
	}
	if err := fn(); err != nil {
		return false, err
	}
	if err := runner.RecordMonitorAction(ctx, action); err != nil {
		return true, fmt.Errorf("record monitor action %s: %w", action.Action, err)
	}
	return true, nil
}

func monitorActionDedupeKey(action, key string) string {
	return action + "\x00" + key
}

func restartableMonitorFinding(code string) bool {
	switch code {
	case factoryhealth.FindingThroughputStall:
		return true
	default:
		return false
	}
}

func monitorRestartWorkers(cfg monitorConfig, metrics factoryhealth.Metrics) int {
	if cfg.targetWorkers > 0 {
		return cfg.targetWorkers
	}
	if metrics.TargetWorkers > 0 {
		return metrics.TargetWorkers
	}
	if metrics.WorkerCount > 0 {
		return metrics.WorkerCount
	}
	return 0
}

func monitorRestartMaxWorkers(cfg monitorConfig, metrics factoryhealth.Metrics, workers int) int {
	var maxWorkers int
	switch {
	case cfg.maxWorkers > 0:
		maxWorkers = cfg.maxWorkers
	case metrics.MaxWorkers > 0:
		maxWorkers = metrics.MaxWorkers
	default:
		maxWorkers = workers
	}
	if workers > maxWorkers {
		return workers
	}
	return maxWorkers
}

type cliMonitorRunner struct{}

func (r *cliMonitorRunner) FactoryHealth(ctx context.Context) (factoryhealth.FactoryHealth, error) {
	return queryFactoryHealth(ctx)
}

func (r *cliMonitorRunner) Resume(ctx context.Context) error {
	return sendMonitorDirective(ctx, "resume", "")
}

func (r *cliMonitorRunner) Pause(ctx context.Context) error {
	return sendMonitorDirective(ctx, "pause", "")
}

func (r *cliMonitorRunner) Scale(ctx context.Context, n int) error {
	return sendMonitorDirective(ctx, "scale", strconv.Itoa(n))
}

func (r *cliMonitorRunner) MaxWorkers(ctx context.Context, n int) error {
	return sendMonitorDirective(ctx, "max-workers", strconv.Itoa(n))
}

func (r *cliMonitorRunner) RestartDaemon(ctx context.Context, workers, maxWorkers int) error {
	if err := sendMonitorDirective(ctx, "restart-daemon", ""); err != nil {
		return err
	}
	if err := waitForMonitorDaemonStopped(ctx, 15*time.Second); err != nil {
		return err
	}
	pidPath, err := startPreflightAndCheckRunning(io.Discard, false)
	if err != nil {
		return err
	}
	if pidPath == "" {
		return nil
	}
	return startFreshSwarm(io.Discard, workers, maxWorkers, "", true, 0, 0, 0, false, false, false, "", defaultCleanlinessStartConfig())
}

func waitForMonitorDaemonStopped(ctx context.Context, timeout time.Duration) error {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, _, statusErr := DaemonStatus(paths.PIDPath, paths.SocketPath)
		if statusErr != nil {
			return statusErr
		}
		switch status {
		case StatusStopped, StatusStale:
			return nil
		case StatusRunning:
			// Keep waiting for the dispatcher to finish its shutdown path.
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for daemon stop after restart directive: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func sendMonitorDirective(ctx context.Context, op, args string) error {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, statusSocketTimeout)
	defer cancel()
	conn, err := dialDispatcher(ctx, paths.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := sendDirectiveWithProvenance(conn, op, args, "monitor", "policy_authorized_recovery"); err != nil {
		return err
	}
	_, err = readACK(conn)
	return err
}
