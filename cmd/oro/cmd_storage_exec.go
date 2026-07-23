package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"oro/pkg/storage"

	"github.com/spf13/cobra"
)

// storageExecExitError preserves a leased child process's exit status through
// Cobra so the top-level CLI can return the same status after cleanup.
type storageExecExitError struct {
	code int
	err  error
}

func (err *storageExecExitError) Error() string { return err.err.Error() }

func (err *storageExecExitError) Unwrap() error { return err.err }

// ExitCode implements the top-level command exit-status contract.
func (err *storageExecExitError) ExitCode() int { return err.code }

// newStorageExecCmd creates the lease-aware command wrapper for repository
// hooks and scripts that are not spawned by an existing runtime owner.
func newStorageExecCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:   "exec --workdir DIR -- argv...",
		Short: "Run one command inside an Oro storage lease",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 || len(args) == 0 {
				return fmt.Errorf("storage exec requires an argv after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageExec(cmd.Context(), workdir, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "working directory for the leased command")
	if err := cmd.MarkFlagRequired("workdir"); err != nil {
		panic(fmt.Sprintf("mark storage exec workdir required: %v", err))
	}
	return cmd
}

func runStorageExec(ctx context.Context, workdir string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	absWorkdir, err := storageExecWorkdir(workdir)
	if err != nil {
		return err
	}
	oroHome, err := resolveOroHome()
	if err != nil {
		return fmt.Errorf("resolve Oro home: %w", err)
	}
	catalog, err := openStorageCatalog(ctx, oroHome)
	if err != nil {
		return fmt.Errorf("open storage exec catalog: %w", err)
	}
	defer func() { err = errors.Join(err, catalog.Close()) }()

	now := time.Now().UTC()
	result, runErr := storage.RunLeasedCommand(ctx, storage.CommandRequest{
		Runtime: storage.RuntimeRequest{
			Catalog: catalog,
			Lease: storage.LeaseRequest{
				ID:           storage.LeaseID(fmt.Sprintf("storage-exec-%d-%d", os.Getpid(), now.UnixNano())),
				ControllerID: "storage-exec",
				OwnerID:      "storage-exec",
				PID:          os.Getpid(),
				ProcessStart: now,
				AcquiredAt:   now,
				HeartbeatAt:  now,
			},
			Env:     os.Environ(),
			Workdir: absWorkdir,
			Policy: storage.StoragePolicy{
				ProjectID:      filepath.Base(absWorkdir),
				RepositoryRoot: absWorkdir,
			},
		},
		Path:   argv[0],
		Args:   argv[1:],
		Dir:    absWorkdir,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if runErr == nil {
		return nil
	}
	var childExit *exec.ExitError
	if errors.As(runErr, &childExit) {
		return &storageExecExitError{code: result.ExitCode, err: runErr}
	}
	return fmt.Errorf("run storage exec command: %w", runErr)
}

func storageExecWorkdir(workdir string) (string, error) {
	if workdir == "" {
		return "", fmt.Errorf("storage exec workdir is empty")
	}
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve storage exec workdir: %w", err)
	}
	info, err := os.Stat(absWorkdir) // #nosec G703 -- an operator explicitly supplies this local workdir to execute within it.
	if err != nil {
		return "", fmt.Errorf("stat storage exec workdir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("storage exec workdir %q is not a directory", absWorkdir)
	}
	return absWorkdir, nil
}
