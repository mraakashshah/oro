package main

import (
	"context"
	"database/sql"
	"fmt"
	"os/signal"
	"syscall"

	"oro/pkg/memory"
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
receives bead assignments, and executes them via claude -p.

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

	spawner := &worker.ClaudeSpawner{}
	w, err := worker.New(id, socketPath, spawner)
	if err != nil {
		return fmt.Errorf("create worker %s: %w", id, err)
	}

	// Wire memory store so [MEMORY] markers and implicit patterns are captured.
	var memStore *memory.Store
	paths, pathsErr := ResolvePaths()
	if pathsErr == nil {
		db, dbErr := openDB(paths.StateDBPath)
		if dbErr == nil {
			defer func() { _ = db.Close() }()
			memStore = openWorkerMemoryStore(db)
			w.SetMemoryStore(memStore)
		}
	}

	if err := w.Run(ctx); err != nil {
		return fmt.Errorf("worker %s: %w", id, err)
	}

	// Persist the embedder vocabulary so the next session starts with the same
	// vector space. Non-fatal: the next session degrades gracefully if missing.
	if memStore != nil {
		_ = memStore.SaveVocab(context.Background())
	}
	return nil
}

// openWorkerMemoryStore creates a memory.Store from an open DB connection.
// It attaches a fresh Embedder and restores the accumulated vocabulary from
// the database so embeddings from prior sessions remain in the same vector
// space. LoadVocab failure is non-fatal: the embedder starts with an empty
// vocab and degrades gracefully (new memories embed correctly; cosine
// similarity against old embeddings may be noisy until vocab re-accumulates).
func openWorkerMemoryStore(db *sql.DB) *memory.Store {
	store := memory.NewStore(db)
	store.SetEmbedder(memory.NewEmbedder())
	_ = store.LoadVocab(context.Background()) // non-fatal: empty vocab is valid
	return store
}
