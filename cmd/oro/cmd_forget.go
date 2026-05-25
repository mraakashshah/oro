package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

type forgetMemoryStore interface {
	GetByID(context.Context, int64) (protocol.Memory, error)
	Delete(context.Context, int64) error
}

// memoryExists checks whether a memory with the given ID exists in the store.
func memoryExists(ctx context.Context, store forgetMemoryStore, id int64) (bool, error) {
	if _, err := store.GetByID(ctx, id); err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("memory %d not found", id)) {
			return false, nil
		}
		return false, fmt.Errorf("check memory exists: %w", err)
	}
	return true, nil
}

func runForget(cmd *cobra.Command, store forgetMemoryStore, args []string) error {
	ctx := context.Background()
	for _, arg := range args {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("forget: invalid id %q: %w", arg, err)
		}
		exists, err := memoryExists(ctx, store, id)
		if err != nil {
			return fmt.Errorf("forget: %w", err)
		}
		if !exists {
			return fmt.Errorf("forget: id %d not found", id)
		}
		if err := store.Delete(ctx, id); err != nil {
			return fmt.Errorf("forget: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Forgot memory %d\n", id)
	}
	return nil
}

// newForgetCmdWithStore creates the "oro forget" subcommand wired to a memory store.
func newForgetCmdWithStore(store forgetMemoryStore) *cobra.Command {
	return &cobra.Command{
		Use:   "forget <id> [id...]",
		Short: "Delete one or more memories by ID",
		Long:  "Remove memories from the store by their numeric IDs.\nPrints confirmation for each deleted memory. Returns an error for nonexistent IDs.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForget(cmd, store, args)
		},
	}
}

// newForgetCmd creates the "oro forget" subcommand.
func newForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <id> [id...]",
		Short: "Delete one or more memories by ID",
		Long:  "Remove memories from the store by their numeric IDs.\nPrints confirmation for each deleted memory. Returns an error for nonexistent IDs.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := defaultMemoryStore()
			if err != nil {
				return fmt.Errorf("forget: %w", err)
			}
			return runForget(cmd, store, args)
		},
	}
}
