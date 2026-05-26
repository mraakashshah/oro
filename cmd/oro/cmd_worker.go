package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"oro/pkg/worker"

	"github.com/spf13/cobra"
)

// newWorkerCmd creates the "oro worker" subcommand.
// This wraps the pkg/worker library into a runnable process that connects
// to the dispatcher's UDS socket and executes beads.
func newWorkerCmd() *cobra.Command {
	var socketPath string
	var workerID string

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run an oro worker process",
		Long: `Starts a worker that connects to the dispatcher UDS socket,
receives bead assignments, and executes them via the configured agent runtime.

This command is typically invoked by the dispatcher, not by humans.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if socketPath == "" {
				return fmt.Errorf("--socket is required")
			}
			if workerID == "" {
				return fmt.Errorf("--id is required")
			}
			return runWorker(cmd.Context(), socketPath, workerID)
		},
	}

	cmd.Flags().StringVar(&socketPath, "socket", "", "path to dispatcher UDS socket (required)")
	cmd.Flags().StringVar(&workerID, "id", "", "worker ID, e.g. w-01 (required)")

	cmd.AddCommand(newWorkerLaunchCmd())
	cmd.AddCommand(newWorkerStopCmd())

	return cmd
}

// runWorker creates a worker instance and runs its event loop.
func runWorker(ctx context.Context, socketPath, id string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	spawner := workerSpawnerForRuntime()
	w, err := worker.NewWithRuntimeSpawner(id, socketPath, spawner)
	if err != nil {
		return fmt.Errorf("create worker %s: %w", id, err)
	}

	// Wire memory store so [MEMORY] markers and implicit patterns are captured.
	var saveVocab func(context.Context) error
	paths, pathsErr := ResolveProjectDBPaths()
	if pathsErr == nil {
		db, dbErr := openStateDB(paths.StateDBPath)
		if dbErr == nil {
			defer func() { _ = db.Close() }()
			memStore := openWorkerMemoryStore(db)
			if memStore != nil {
				w.SetMemoryStore(memStore)
				saveVocab = memStore.SaveVocab
			}
		}
	}
	w.SetExtractSpawner(newWorkerMemoryExtractSpawner())

	if err := w.Run(ctx); err != nil {
		return fmt.Errorf("worker %s: %w", id, err)
	}

	// Persist the embedder vocabulary so the next session starts with the same
	// vector space. Non-fatal: the next session degrades gracefully if missing.
	if saveVocab != nil {
		_ = saveVocab(context.Background())
	}
	return nil
}

func workerSpawnerForRuntime() worker.RuntimeStreamingSpawner {
	return worker.NewRuntimeSpawnerRouter(newClaudeWorkerSpawner(), newCodexWorkerSpawner())
}
