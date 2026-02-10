package main

import (
	"context"
	"fmt"
	"strconv"

	"oro/pkg/memory"

	"github.com/spf13/cobra"
)

// memoryExists checks whether a memory with the given ID exists in the store.
func memoryExists(ctx context.Context, store *memory.Store, id int64) (bool, error) {
	all, err := store.List(ctx, memory.ListOpts{Limit: 1000})
	if err != nil {
		return false, fmt.Errorf("check memory exists: %w", err)
	}
	for _, m := range all {
		if m.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// newForgetCmdWithStore creates the "oro forget" subcommand wired to a memory.Store.
func newForgetCmdWithStore(store *memory.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "forget <id> [id...]",
		Short: "Delete one or more memories by ID",
		Long:  "Remove memories from the store by their numeric IDs.\nPrints confirmation for each deleted memory. Returns an error for nonexistent IDs.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			ctx := context.Background()
			for _, arg := range args {
				id, parseErr := strconv.ParseInt(arg, 10, 64)
				if parseErr != nil {
					return fmt.Errorf("forget: invalid id %q: %w", arg, parseErr)
				}
				exists, checkErr := memoryExists(ctx, store, id)
				if checkErr != nil {
					return fmt.Errorf("forget: %w", checkErr)
				}
				if !exists {
					return fmt.Errorf("forget: id %d not found", id)
				}
				if delErr := store.Delete(ctx, id); delErr != nil {
					return fmt.Errorf("forget: %w", delErr)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Forgot memory %d\n", id)
			}
			return nil
		},
	}
}
