package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/factoryhealth"

	"github.com/spf13/cobra"
)

func newHealthCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Show factory health findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			health, err := queryFactoryHealth(cmd.Context())
			if err != nil {
				return err
			}
			if jsonOut {
				return writeFactoryHealthJSON(cmd.OutOrStdout(), health)
			}
			formatFactoryHealth(cmd.OutOrStdout(), health)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit health as JSON")
	return cmd
}

func queryFactoryHealth(ctx context.Context) (factoryhealth.FactoryHealth, error) {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("resolve paths: %w", err)
	}
	status, pid, err := DaemonStatus(paths.PIDPath, paths.SocketPath)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("get daemon status: %w", err)
	}
	if status == StatusRunning {
		health, err := queryDispatcherHealth(ctx, paths.SocketPath)
		if err == nil {
			return health, nil
		}
	}
	return loadLocalFactoryHealth(ctx, paths.StateDBPath, status == StatusRunning, pid, string(status))
}

func queryDispatcherHealth(ctx context.Context, sockPath string) (factoryhealth.FactoryHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, statusSocketTimeout)
	defer cancel()

	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return factoryhealth.FactoryHealth{}, err
	}
	defer conn.Close()
	if err := sendDirective(conn, "health", ""); err != nil {
		return factoryhealth.FactoryHealth{}, err
	}
	ack, err := readACK(conn)
	if err != nil {
		return factoryhealth.FactoryHealth{}, err
	}
	var health factoryhealth.FactoryHealth
	if err := json.Unmarshal([]byte(ack.Detail), &health); err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("parse health response: %w", err)
	}
	return health, nil
}

func loadLocalFactoryHealth(ctx context.Context, stateDBPath string, daemonRunning bool, pid int, dispatcherState string) (factoryhealth.FactoryHealth, error) {
	db, err := openStateDB(stateDBPath)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("open state db: %w", err)
	}
	defer db.Close()

	now := time.Now()
	activeAssignments, err := factoryhealth.LoadActiveAssignments(ctx, db, now)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("load active assignments: %w", err)
	}
	openQG, qgOccurrences, topFingerprints, err := factoryhealth.LoadQGMetrics(ctx, db)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("load qg metrics: %w", err)
	}
	openRecoveryQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, db)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("load recovery quarantine metrics: %w", err)
	}
	throughput, err := factoryhealth.LoadThroughputMetrics(ctx, db, now, 30*time.Minute)
	if err != nil {
		return factoryhealth.FactoryHealth{}, fmt.Errorf("load throughput metrics: %w", err)
	}
	readyQueue := 0
	store := beadstore.NewSQLiteStore(db)
	if ready, readyErr := store.Ready(ctx); readyErr == nil {
		readyQueue = len(ready)
	}
	return factoryhealth.Evaluate(factoryhealth.Snapshot{
		DaemonRunning:           daemonRunning,
		DaemonPID:               pid,
		DispatcherState:         dispatcherState,
		ReadyQueue:              readyQueue,
		ActiveAssignments:       activeAssignments,
		OpenQGIncidents:         openQG,
		QGOccurrences30m:        qgOccurrences,
		QGTopFingerprints:       topFingerprints,
		OpenRecoveryQuarantines: openRecoveryQuarantines,
		Throughput:              throughput,
	}), nil
}

func writeFactoryHealthJSON(w io.Writer, health factoryhealth.FactoryHealth) error {
	data, err := json.Marshal(health)
	if err != nil {
		return fmt.Errorf("marshal health: %w", err)
	}
	fmt.Fprintln(w, string(data))
	return nil
}

func formatFactoryHealth(w io.Writer, health factoryhealth.FactoryHealth) {
	fmt.Fprintf(w, "health: %s\n", health.State)
	fmt.Fprintf(w, "posture: %s\n", health.Posture)
	fmt.Fprintf(w, "workers: %d total, %d active, %d idle\n",
		health.Metrics.WorkerCount, health.Metrics.ActiveWorkers, health.Metrics.IdleWorkers)
	fmt.Fprintf(w, "queue: %d ready\n", health.Metrics.ReadyQueue)
	fmt.Fprintf(w, "assignments: %d active", health.Metrics.ActiveAssignments)
	if health.Metrics.OrphanAssignments > 0 {
		fmt.Fprintf(w, ", %d orphan", health.Metrics.OrphanAssignments)
	}
	fmt.Fprintln(w)
	if health.Metrics.OpenQGIncidents > 0 || health.Metrics.QGOccurrences30m > 0 {
		fmt.Fprintf(w, "qg: %d open, %d occurrences in 30m\n", health.Metrics.OpenQGIncidents, health.Metrics.QGOccurrences30m)
	}
	if health.Metrics.OpenRecoveryQuarantines > 0 {
		fmt.Fprintf(w, "recovery: %d quarantine(s) open\n", health.Metrics.OpenRecoveryQuarantines)
	}
	if len(health.Findings) == 0 {
		fmt.Fprintln(w, "findings: none")
		return
	}
	fmt.Fprintln(w, "findings:")
	for _, finding := range health.Findings {
		fmt.Fprintf(w, "  %s [%s] %s", finding.Code, finding.Severity, finding.Message)
		if finding.RecommendedAction != "" {
			fmt.Fprintf(w, " | action: %s", finding.RecommendedAction)
		}
		fmt.Fprintln(w)
	}
}
