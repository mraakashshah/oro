package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"oro/pkg/janitor"
	"oro/pkg/storage"

	"github.com/spf13/cobra"
)

func newJanitorDetectCmd() *cobra.Command {
	var detector, targetBranch string
	var projectScript bool
	cmd := &cobra.Command{
		Use:    "janitor:detect --detector <name>",
		Short:  "Rerun one janitor detector",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			candidates, err := janitorDetectorCandidates(
				cmd.Context(), currentRepoRoot(), targetBranch, detector, projectScript,
			)
			if err != nil {
				return fmt.Errorf("run janitor detector: %w", err)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(candidates); err != nil {
				return fmt.Errorf("write janitor detector result: %w", err)
			}
			if len(candidates) > 0 {
				return fmt.Errorf("janitor detector %q found %d candidate(s)", detector, len(candidates))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&detector, "detector", "", "detector name")
	cmd.Flags().StringVar(&targetBranch, "target-branch", "", "branch inspected by the CI detector")
	cmd.Flags().BoolVar(&projectScript, "project-script", false, "rerun the project-owned detector script")
	_ = cmd.MarkFlagRequired("detector")
	return cmd
}

func janitorDetectorCandidates(
	ctx context.Context,
	worktree, targetBranch, detector string,
	projectScript bool,
) ([]janitor.Candidate, error) {
	runtime, catalog, err := janitorCommandRuntime(ctx, worktree)
	if err != nil {
		return nil, err
	}
	defer func() { _ = catalog.Close() }()
	option := janitor.WithRuntime(runtime)
	if !projectScript {
		candidates, err := janitor.RunBuiltin(ctx, worktree, targetBranch, detector, option)
		if err != nil {
			return nil, fmt.Errorf("run built-in janitor detector: %w", err)
		}
		return candidates, nil
	}
	candidates, skippedLines, found, err := janitor.RunDetectScript(ctx, worktree, option)
	if err != nil {
		return nil, fmt.Errorf("run project janitor detector: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("project janitor detector script not found")
	}
	if len(skippedLines) > 0 {
		return nil, fmt.Errorf("project janitor detector emitted %d malformed record(s)", len(skippedLines))
	}
	matching := make([]janitor.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Detector == detector {
			matching = append(matching, candidate)
		}
	}
	return matching, nil
}

func janitorCommandRuntime(ctx context.Context, worktree string) (storage.RuntimeRequest, *storage.Catalog, error) {
	oroHome, err := resolveOroHome()
	if err != nil {
		return storage.RuntimeRequest{}, nil, fmt.Errorf("resolve oro home for janitor detector: %w", err)
	}
	catalog, err := openStorageCatalog(ctx, oroHome)
	if err != nil {
		return storage.RuntimeRequest{}, nil, err
	}
	now := time.Now().UTC()
	return storage.RuntimeRequest{
		Catalog: catalog,
		Lease: storage.LeaseRequest{
			ControllerID: "janitor-cli",
			OwnerID:      "janitor-detector",
			PID:          os.Getpid(),
			ProcessStart: now,
			AcquiredAt:   now,
			HeartbeatAt:  now,
		},
		Env:     os.Environ(),
		Workdir: worktree,
		Policy: storage.StoragePolicy{
			ProjectID:      filepath.Base(worktree),
			RepositoryRoot: worktree,
		},
	}, catalog, nil
}
