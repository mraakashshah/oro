package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newBeadReadyCmd(store beadstore.Store) *cobra.Command {
	return newBeadListLikeCmd(store, "ready", "List unblocked open beads", cobra.NoArgs, func(ctx context.Context, s beadstore.Store, _ *cobra.Command) ([]protocol.Bead, error) {
		return s.Ready(ctx)
	})
}

func newBeadListCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadListLikeCmd(store, "list", "List beads with optional filters", cobra.NoArgs, listBeadsForCmd)
	cmd.Long = `List beads with optional filters.

By default, shows the top 20 in-progress and unblocked open beads as a human-readable table with aligned columns (ID, STATUS, PRI, TYPE, UPDATED, TITLE).

Use --json for stable machine-readable JSON output suitable for scripting and automation.`
	cmd.Flags().String("status", "", "filter by status")
	cmd.Flags().String("parent", "", "filter by parent bead ID")
	cmd.Flags().String("tag", "", "filter by tag")
	cmd.Flags().Int("limit", 0, "maximum beads to return")
	return cmd
}

func newBeadShowCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}

			bead, err := s.Show(cmd.Context(), args[0])
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "show", fmt.Errorf("show bead %s: %w", args[0], err))
			}
			if bead == nil {
				return writeBeadCommandErrorIfJSON(cmd, "show", fmt.Errorf("bead %s not found", args[0]))
			}

			jsonOutput, err := cmd.Flags().GetBool("json")
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "flags", fmt.Errorf("read --json: %w", err))
			}
			if jsonOutput {
				return writeBeadJSON(cmd, *bead)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tP%d\t%s\n", bead.ID, bead.Status, bead.Priority, bead.Title)
			return nil
		},
	}
	cmd.Flags().Bool("long", false, "show full bead details")
	return cmd
}

func createBeadFromParams(ctx context.Context, s beadstore.Store, params beadstore.CreateParams) (*protocol.Bead, error) {
	bead, err := s.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("Store.Create: %w", err)
	}
	if bead == nil {
		return nil, fmt.Errorf("Store.Create: nil bead returned")
	}
	return bead, nil
}

func newBeadCreateCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bead",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBeadCreate(cmd, store)
		},
	}
	cmd.Flags().String("id", "", "explicit bead ID")
	cmd.Flags().String("title", "", "bead title")
	cmd.Flags().String("type", "task", "bead type")
	cmd.Flags().Int("priority", 2, "bead priority; 0 is highest")
	cmd.Flags().String("parent", "", "parent bead ID")
	cmd.Flags().String("description", "", "bead description")
	cmd.Flags().String("acceptance", "", "acceptance criteria")
	cmd.Flags().String("acceptance-criteria", "", "acceptance criteria")
	cmd.Flags().Int("estimate", 0, "estimated minutes")
	cmd.Flags().StringArray("tag", nil, "tag to attach; repeatable")
	cmd.Flags().String("tier", "", "routing tier: fast, balanced, deep, or background")
	return cmd
}

func runBeadCreate(cmd *cobra.Command, store beadstore.Store) error {
	s, err := resolveBeadStore(store)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "store", err)
	}

	acceptance, err := stringFlag(cmd, "acceptance")
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "flags", err)
	}
	if acceptanceCriteria, err := stringFlag(cmd, "acceptance-criteria"); err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "flags", err)
	} else if acceptanceCriteria != "" {
		acceptance = acceptanceCriteria
	}
	typeName := strings.TrimSpace(mustStringFlag(cmd, "type"))
	if strings.EqualFold(typeName, "premortem") {
		return writeBeadCommandErrorIfJSON(cmd, "create", fmt.Errorf("creating premortem beads from the CLI is no longer supported"))
	}

	tier, err := parseTierFlag(cmd)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "flags", err)
	}
	params := beadstore.CreateParams{
		Title:              mustStringFlag(cmd, "title"),
		Type:               typeName,
		Priority:           mustIntFlag(cmd, "priority"),
		ParentID:           mustStringFlag(cmd, "parent"),
		Description:        mustStringFlag(cmd, "description"),
		AcceptanceCriteria: acceptance,
		EstimatedMinutes:   mustIntFlag(cmd, "estimate"),
		ID:                 mustStringFlag(cmd, "id"),
		Tags:               mustStringArrayFlag(cmd, "tag"),
		Tier:               string(tier),
	}
	bead, err := createBeadFromParams(cmd.Context(), s, params)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "create", err)
	}

	if isJSONOutput(cmd) {
		return writeBeadJSON(cmd, *bead)
	}
	fmt.Fprintln(cmd.OutOrStdout(), bead.ID)
	return nil
}

// parseTierFlag reads the --tier flag and validates it. Returns an empty Tier
// when the flag is absent or empty.
func parseTierFlag(cmd *cobra.Command) (protocol.Tier, error) {
	raw, err := stringFlag(cmd, "tier")
	if err != nil || raw == "" {
		return "", err
	}
	tier, ok := protocol.ParseTier(raw)
	if !ok {
		return "", fmt.Errorf("unknown tier %q: must be fast, balanced, deep, or background", raw)
	}
	return tier, nil
}

func newBeadUpdateCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}

			params := beadstore.UpdateParams{}
			if cmd.Flags().Changed("status") {
				value := mustStringFlag(cmd, "status")
				params.Status = &value
			}
			if cmd.Flags().Changed("priority") {
				value := mustIntFlag(cmd, "priority")
				params.Priority = &value
			}
			if cmd.Flags().Changed("type") {
				value := mustStringFlag(cmd, "type")
				params.Type = &value
			}
			if cmd.Flags().Changed("parent") {
				value := mustStringFlag(cmd, "parent")
				params.ParentID = &value
			}
			if cmd.Flags().Changed("owner") {
				value := mustStringFlag(cmd, "owner")
				params.Owner = &value
			}
			if cmd.Flags().Changed("acceptance") {
				value := mustStringFlag(cmd, "acceptance")
				params.AcceptanceCriteria = &value
			}
			if cmd.Flags().Changed("notes") {
				value := mustStringFlag(cmd, "notes")
				params.Notes = &value
			}
			if err := s.Update(cmd.Context(), args[0], params); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "update", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
	cmd.Flags().String("status", "", "new status")
	cmd.Flags().Int("priority", -1, "new priority")
	cmd.Flags().String("type", "", "new bead type")
	cmd.Flags().String("parent", "", "new parent bead ID")
	cmd.Flags().String("notes", "", "notes to append")
	cmd.Flags().String("acceptance", "", "acceptance criteria")
	cmd.Flags().String("owner", "", "new owner")
	return cmd
}

func newBeadCloseCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardWorkerSelfClose(args[0]); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "close", err)
			}
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			if err := s.Close(cmd.Context(), args[0], mustStringFlag(cmd, "reason")); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "close", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
	cmd.Flags().String("reason", "", "close reason")
	return cmd
}

func newBeadDeleteCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			reason := mustStringFlag(cmd, "reason")
			if err := s.Delete(cmd.Context(), args[0], reason); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "delete", err)
			}
			if reason == "" {
				reason = "deleted by user"
			}
			if isJSONOutput(cmd) {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(map[string]any{
					"id":      args[0],
					"deleted": true,
					"reason":  reason,
				}); err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "delete", fmt.Errorf("encode delete JSON: %w", err))
				}
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().String("reason", "", "delete reason")
	return cmd
}

// guardWorkerSelfClose blocks an Oro worker subprocess from closing its
// currently assigned bead via the CLI. The dispatcher is the sole closer for
// assigned worker beads (including manual-integration mode); a worker should
// emit DONE and let the dispatcher run the close path. See oro-t5ha.
func guardWorkerSelfClose(target string) error {
	if os.Getenv("ORO_WORKER") != "1" {
		return nil
	}
	assigned := os.Getenv("ORO_WORKER_BEAD_ID")
	if assigned == "" || assigned != target {
		return nil
	}
	return fmt.Errorf("refusing self-close of assigned bead %s: workers must emit DONE and let the dispatcher integrate (ORO_WORKER_BEAD_ID guard, oro-t5ha)", target)
}

// guardWorkerDepAddSelf blocks an Oro worker subprocess from adding a
// dependency edge whose source matches its own assigned bead. This stops
// the leaf-bead self-decomposition pattern (oro-xs1a / oro-qafy) where a
// worker assigned a non-epic bead injected phantom blocks-deps onto itself,
// rendering the bead unreachable in the queue. Legitimate epic decomposition
// adds deps on the parent epic (source != ORO_WORKER_BEAD_ID), which is
// allowed.
func guardWorkerDepAddSelf(source string) error {
	if os.Getenv("ORO_WORKER") != "1" {
		return nil
	}
	assigned := os.Getenv("ORO_WORKER_BEAD_ID")
	if assigned == "" || assigned != source {
		return nil
	}
	return fmt.Errorf("refusing self-dep-add on assigned bead %s: workers must not block their own assignment via the CLI (ORO_WORKER_BEAD_ID guard, oro-xs1a/oro-qafy)", source)
}

func newBeadReopenCmd(store beadstore.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <id>",
		Short: "Reopen a closed bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			status := "open"
			if err := s.Update(cmd.Context(), args[0], beadstore.UpdateParams{Status: &status}); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "reopen", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
}

func newBeadDeferCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "defer <id>",
		Short: "Defer a bead until a future time",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			until := mustStringFlag(cmd, "until")
			if until == "" {
				return writeBeadCommandErrorIfJSON(cmd, "flags", fmt.Errorf("--until is required"))
			}
			if _, err := time.Parse(time.RFC3339Nano, until); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "flags", fmt.Errorf("parse --until: %w", err))
			}
			deferer, ok := s.(interface {
				Defer(context.Context, string, string) error
			})
			if !ok {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", fmt.Errorf("%s is not supported by this bead store", cmd.CommandPath()))
			}
			if err := deferer.Defer(cmd.Context(), args[0], until); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "defer", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
	cmd.Flags().String("until", "", "RFC3339 timestamp when the bead becomes ready again")
	return cmd
}

func newBeadUndeferCmd(store beadstore.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "undefer <id>",
		Short: "Clear a bead deferral",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			undeferer, ok := s.(interface {
				Undefer(context.Context, string) error
			})
			if !ok {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", fmt.Errorf("%s is not supported by this bead store", cmd.CommandPath()))
			}
			if err := undeferer.Undefer(cmd.Context(), args[0]); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "undefer", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
}

func newBeadBlockedCmd(store beadstore.Store) *cobra.Command {
	return newBeadListLikeCmd(store, "blocked", "List blocked beads", cobra.NoArgs, func(ctx context.Context, s beadstore.Store, _ *cobra.Command) ([]protocol.Bead, error) {
		return s.Blocked(ctx)
	})
}

func newBeadClosedCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadListLikeCmd(store, "closed", "List recently closed beads", cobra.NoArgs, func(ctx context.Context, s beadstore.Store, cmd *cobra.Command) ([]protocol.Bead, error) {
		return s.Closed(ctx, mustIntFlag(cmd, "limit"))
	})
	cmd.Flags().Int("limit", 50, "maximum closed beads to return")
	return cmd
}

func newBeadDepCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dep",
		Short: "Manage bead dependencies",
	}

	cmd.AddCommand(
		newBeadDepAddCmd(store),
		newBeadDepRemoveCmd(store),
		newBeadDepListCmd(store),
		newBeadDepCyclesCmd(store),
	)

	return cmd
}

func newBeadDepAddCmd(store beadstore.Store) *cobra.Command {
	addCmd := &cobra.Command{
		Use:   "add <bead-id> <depends-on-id>",
		Short: "Add a dependency",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardWorkerDepAddSelf(args[0]); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "guard", err)
			}
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			deps, ok := s.(interface {
				AddDependency(context.Context, string, string, string) error
			})
			if !ok {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", fmt.Errorf("%s is not supported by this bead store", cmd.CommandPath()))
			}
			if err := deps.AddDependency(cmd.Context(), args[0], args[1], mustStringFlag(cmd, "type")); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "dep_add", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
	addCmd.Flags().String("type", "blocks", "dependency type")
	return addCmd
}

func newBeadDepRemoveCmd(store beadstore.Store) *cobra.Command {
	rmCmd := &cobra.Command{
		Use:   "rm <bead-id> <depends-on-id>",
		Short: "Remove a dependency",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			deps, ok := s.(interface {
				RemoveDependency(context.Context, string, string) error
			})
			if !ok {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", fmt.Errorf("%s is not supported by this bead store", cmd.CommandPath()))
			}
			if err := deps.RemoveDependency(cmd.Context(), args[0], args[1]); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "dep_rm", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
	return rmCmd
}

func newBeadDepListCmd(store beadstore.Store) *cobra.Command {
	listCmd := &cobra.Command{
		Use:   "list <bead-id>",
		Short: "List dependencies for a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			depsStore, ok := s.(interface {
				ListDependencies(context.Context, string) ([]protocol.Dependency, error)
			})
			if !ok {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", fmt.Errorf("%s is not supported by this bead store", cmd.CommandPath()))
			}
			deps, err := depsStore.ListDependencies(cmd.Context(), args[0])
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "dep_list", err)
			}
			return writeDependencies(cmd, deps)
		},
	}
	return listCmd
}

func newBeadDepCyclesCmd(store beadstore.Store) *cobra.Command {
	cyclesCmd := &cobra.Command{
		Use:           "cycles",
		Short:         "Find dependency cycles",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			cycles, err := s.DependencyCycles(cmd.Context())
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "dep_cycles", err)
			}
			if err := writeDependencyCycles(cmd, cycles); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "dep_cycles", err)
			}
			if len(cycles) > 0 {
				return fmt.Errorf("dependency cycles found")
			}
			return nil
		},
	}
	return cyclesCmd
}

func newBeadNoteAddCmd(store beadstore.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "add <bead-id> <text>",
		Short: "Add a note to a bead",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			note := args[1]
			if err := s.Update(cmd.Context(), args[0], beadstore.UpdateParams{Notes: &note}); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "note_add", err)
			}
			return writeBeadMutationResult(cmd, s, args[0])
		},
	}
}

func newBeadStatusCmd(store beadstore.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bead status counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			counts, err := beadStatusCounts(cmd.Context(), s)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "status", err)
			}
			if isJSONOutput(cmd) {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(counts); err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "status", fmt.Errorf("encode status JSON: %w", err))
				}
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "open\t%d\nin_progress\t%d\nclosed\t%d\n", counts.Open, counts.InProgress, counts.Closed)
			return nil
		},
	}
}

func newBeadExportCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a bead snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			data, err := s.Export(cmd.Context())
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "export", err)
			}
			format := mustStringFlag(cmd, "format")
			outPath := mustStringFlag(cmd, "out")
			if isJSONOutput(cmd) || format == "json" {
				data, err = beadJSONLToJSONArray(data)
				if err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "export", err)
				}
			}
			if outPath != "" {
				if err := os.WriteFile(outPath, data, 0o600); err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "export", fmt.Errorf("write export %s: %w", outPath, err))
				}
				return nil
			}
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "export", fmt.Errorf("write export output: %w", err))
			}
			return nil
		},
	}
	cmd.Flags().String("out", "", "output path")
	cmd.Flags().String("format", "jsonl", "output format: jsonl or json")
	return cmd
}

func writeDependencies(cmd *cobra.Command, deps []protocol.Dependency) error {
	if isJSONOutput(cmd) {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if deps == nil {
			deps = []protocol.Dependency{}
		}
		if err := enc.Encode(deps); err != nil {
			return fmt.Errorf("encode dependencies JSON: %w", err)
		}
		return nil
	}
	for _, dep := range deps {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", dep.IssueID, dep.DependsOnID, dep.Type)
	}
	return nil
}

func writeDependencyCycles(cmd *cobra.Command, cycles []beadstore.Cycle) error {
	if isJSONOutput(cmd) {
		if cycles == nil {
			cycles = []beadstore.Cycle{}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string][]beadstore.Cycle{"cycles": cycles}); err != nil {
			return fmt.Errorf("encode dependency cycles JSON: %w", err)
		}
		return nil
	}
	for _, cycle := range cycles {
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(cycle, " → "))
	}
	return nil
}

func beadStatusCounts(ctx context.Context, s beadstore.Store) (beadstore.StatusCounts, error) {
	if counter, ok := s.(interface {
		CountByStatus(context.Context) (beadstore.StatusCounts, error)
	}); ok {
		counts, err := counter.CountByStatus(ctx)
		if err != nil {
			return beadstore.StatusCounts{}, fmt.Errorf("count bead statuses: %w", err)
		}
		return counts, nil
	}
	ready, err := s.Ready(ctx)
	if err != nil {
		return beadstore.StatusCounts{}, fmt.Errorf("list ready beads: %w", err)
	}
	blocked, err := s.Blocked(ctx)
	if err != nil {
		return beadstore.StatusCounts{}, fmt.Errorf("list blocked beads: %w", err)
	}
	inProgress, err := s.InProgress(ctx)
	if err != nil {
		return beadstore.StatusCounts{}, fmt.Errorf("list in-progress beads: %w", err)
	}
	closed, err := s.Closed(ctx, 1_000_000)
	if err != nil {
		return beadstore.StatusCounts{}, fmt.Errorf("list closed beads: %w", err)
	}
	return beadstore.StatusCounts{
		Open:       len(ready) + len(blocked),
		InProgress: len(inProgress),
		Closed:     len(closed),
	}, nil
}

type beadListFunc func(context.Context, beadstore.Store, *cobra.Command) ([]protocol.Bead, error)

func newBeadListLikeCmd(store beadstore.Store, use, short string, args cobra.PositionalArgs, list beadListFunc) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := resolveBeadStore(store)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "store", err)
			}
			beads, err := list(cmd.Context(), s, cmd)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "list", err)
			}
			if isJSONOutput(cmd) {
				return writeBeadsJSON(cmd, beads)
			}
			return writeBeadListHuman(cmd.OutOrStdout(), beads, time.Now())
		},
	}
}

func listBeadsForCmd(ctx context.Context, s beadstore.Store, cmd *cobra.Command) ([]protocol.Bead, error) {
	status := mustStringFlag(cmd, "status")
	parent := mustStringFlag(cmd, "parent")
	tag := mustStringFlag(cmd, "tag")
	limit := mustIntFlag(cmd, "limit")

	if parent != "" && tag != "" {
		beads, err := s.FindByParentAndTag(ctx, parent, tag)
		return applyBeadListFilters(beads, status, parent, limit), err
	}

	// Default view: no filtering flags set → InProgress ++ Ready, deduped, capped at 20.
	// Explicit --limit overrides the default cap (0 = unlimited).
	if status == "" && parent == "" && tag == "" {
		effectiveLimit := 20
		if cmd.Flags().Changed("limit") {
			effectiveLimit = limit
		}
		return listTopUnfinished(ctx, s, effectiveLimit)
	}

	var (
		beads []protocol.Bead
		err   error
	)
	switch status {
	case "":
		beads, err = listExportedBeads(ctx, s)
	case "open":
		beads, err = listBeadsByExportedStatus(ctx, s, "open")
	case "blocked":
		beads, err = s.Blocked(ctx)
	case "closed":
		if limit <= 0 {
			limit = 50
		}
		beads, err = s.Closed(ctx, limit)
	case "in_progress":
		beads, err = s.InProgress(ctx)
	default:
		beads, err = s.Ready(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list beads for status %q: %w", status, err)
	}
	return applyBeadListFilters(beads, "", parent, limit), nil
}

// listTopUnfinished returns InProgress beads followed by Ready beads, deduplicated,
// capped at limit (0 = unlimited). This is the default view for `oro task list`.
func listTopUnfinished(ctx context.Context, s beadstore.Store, limit int) ([]protocol.Bead, error) {
	inProgress, err := s.InProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("list in_progress beads: %w", err)
	}
	ready, err := s.Ready(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ready beads: %w", err)
	}

	seen := make(map[string]struct{}, len(inProgress))
	for _, b := range inProgress {
		seen[b.ID] = struct{}{}
	}

	result := make([]protocol.Bead, 0, len(inProgress)+len(ready))
	result = append(result, inProgress...)
	for _, b := range ready {
		if _, dup := seen[b.ID]; !dup {
			result = append(result, b)
		}
	}

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func listExportedBeads(ctx context.Context, s beadstore.Store) ([]protocol.Bead, error) {
	data, err := s.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("export beads for list: %w", err)
	}
	return decodeBeadExportJSONL(data)
}

func listBeadsByExportedStatus(ctx context.Context, s beadstore.Store, status string) ([]protocol.Bead, error) {
	beads, err := listExportedBeads(ctx, s)
	if err != nil {
		return nil, err
	}
	return filterBeadsByStatus(beads, status), nil
}

func filterBeadsByStatus(beads []protocol.Bead, status string) []protocol.Bead {
	filtered := beads[:0]
	for _, bead := range beads {
		if bead.Status == status {
			filtered = append(filtered, bead)
		}
	}
	return filtered
}

func applyBeadListFilters(beads []protocol.Bead, status, parent string, limit int) []protocol.Bead {
	filtered := make([]protocol.Bead, 0, len(beads))
	for _, bead := range beads {
		if status != "" && bead.Status != status {
			continue
		}
		if parent != "" && bead.Epic != parent {
			continue
		}
		filtered = append(filtered, bead)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func resolveBeadStore(store beadstore.Store) (beadstore.Store, error) {
	if store != nil {
		return store, nil
	}
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve bead store paths: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.StateDBPath), 0o700); err != nil {
		return nil, fmt.Errorf("create bead store dir: %w", err)
	}
	// Use openStateDB so native beadstore migrations are applied — bare
	// OpenSQLiteStore only runs MigrateBeadSchema.
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return nil, fmt.Errorf("open bead store: %w", err)
	}
	return beadstore.NewSQLiteStore(db), nil
}

type beadJSON struct {
	ID                 string                `json:"id"`
	Title              string                `json:"title"`
	ContractVersion    int                   `json:"contract_version"`
	Draft              bool                  `json:"draft"`
	Status             any                   `json:"status"`
	Priority           int                   `json:"priority"`
	ParentID           any                   `json:"parent_id"`
	Type               any                   `json:"type"`
	Model              any                   `json:"model"`
	Tier               any                   `json:"tier"`
	WorkerID           any                   `json:"worker_id"`
	ContextPercent     any                   `json:"context_percent"`
	LastHeartbeat      any                   `json:"last_heartbeat"`
	GitDiff            any                   `json:"git_diff"`
	Memory             any                   `json:"memory"`
	EstimatedMinutes   any                   `json:"estimated_minutes"`
	AcceptanceCriteria any                   `json:"acceptance_criteria"`
	Dependencies       []protocol.Dependency `json:"dependencies"`
	UpdatedAt          any                   `json:"updated_at"`
	ClosedAt           any                   `json:"closed_at"`
	CreatedAt          any                   `json:"created_at"`
	Description        any                   `json:"description"`
	CloseReason        any                   `json:"close_reason"`
	Owner              any                   `json:"owner"`
	Notes              any                   `json:"notes"`
	Tags               []string              `json:"tags"`
	Metadata           map[string]any        `json:"metadata"`
	Labels             []string              `json:"labels"`
}

type beadCommandErrorJSON struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Command string `json:"command"`
}

func writeBeadJSON(cmd *cobra.Command, bead protocol.Bead) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(beadJSONFromProtocol(bead)); err != nil {
		return fmt.Errorf("encode bead JSON: %w", err)
	}
	return nil
}

func writeBeadsJSON(cmd *cobra.Command, beads []protocol.Bead) error {
	out := make([]beadJSON, 0, len(beads))
	for _, bead := range beads {
		out = append(out, beadJSONFromProtocol(bead))
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode beads JSON: %w", err)
	}
	return nil
}

func writeBeadCommandErrorIfJSON(cmd *cobra.Command, code string, err error) error {
	if !isJSONOutput(cmd) {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(beadCommandErrorJSON{
		OK:      false,
		Error:   code,
		Message: err.Error(),
		Command: cmd.CommandPath(),
	}); err != nil {
		return fmt.Errorf("encode bead command error JSON: %w", err)
	}
	return nil
}

func writeBeadMutationResult(cmd *cobra.Command, store beadstore.Store, id string) error {
	bead, err := store.Show(cmd.Context(), id)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "show", err)
	}
	if bead == nil {
		return writeBeadCommandErrorIfJSON(cmd, "show", fmt.Errorf("bead %s not found", id))
	}
	if isJSONOutput(cmd) {
		return writeBeadJSON(cmd, *bead)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tP%d\t%s\n", bead.ID, bead.Status, bead.Priority, bead.Title)
	return nil
}

func beadJSONLToJSONArray(data []byte) ([]byte, error) {
	beads, err := decodeBeadExportJSONL(data)
	if err != nil {
		return nil, err
	}
	out := make([]beadJSON, 0, len(beads))
	for _, bead := range beads {
		out = append(out, beadJSONFromProtocol(bead))
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode export JSON: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeBeadExportJSONL(data []byte) ([]protocol.Bead, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	beads := []protocol.Bead{}
	for {
		var bead protocol.Bead
		if err := decoder.Decode(&bead); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode export JSONL: %w", err)
		}
		beads = append(beads, bead)
	}
	return beads, nil
}

func isJSONOutput(cmd *cobra.Command) bool {
	jsonOutput, err := cmd.Flags().GetBool("json")
	if err == nil {
		return jsonOutput
	}
	jsonOutput, err = cmd.InheritedFlags().GetBool("json")
	if err == nil {
		return jsonOutput
	}
	if cmd.Root() != nil {
		jsonOutput, err = cmd.Root().PersistentFlags().GetBool("json")
		return err == nil && jsonOutput
	}
	return false
}

func stringFlag(cmd *cobra.Command, name string) (string, error) {
	value, err := cmd.Flags().GetString(name)
	if err != nil {
		return "", fmt.Errorf("read --%s: %w", name, err)
	}
	return value, nil
}

func mustStringFlag(cmd *cobra.Command, name string) string {
	value, err := stringFlag(cmd, name)
	if err != nil {
		panic(err)
	}
	return value
}

func mustIntFlag(cmd *cobra.Command, name string) int {
	value, err := cmd.Flags().GetInt(name)
	if err != nil {
		panic(fmt.Errorf("read --%s: %w", name, err))
	}
	return value
}

func mustStringArrayFlag(cmd *cobra.Command, name string) []string {
	value, err := cmd.Flags().GetStringArray(name)
	if err != nil {
		panic(fmt.Errorf("read --%s: %w", name, err))
	}
	return value
}

func beadJSONFromProtocol(bead protocol.Bead) beadJSON {
	return beadJSON{
		ID:                 bead.ID,
		Title:              bead.Title,
		ContractVersion:    bead.ContractVersion,
		Draft:              bead.Draft,
		Status:             nullableString(bead.Status),
		Priority:           bead.Priority,
		ParentID:           nullableString(bead.Epic),
		Type:               nullableString(bead.Type),
		Model:              nullableString(bead.Model),
		Tier:               nullableString(string(bead.Tier)),
		WorkerID:           nullableString(bead.WorkerID),
		ContextPercent:     nullablePositiveInt(bead.ContextPercent),
		LastHeartbeat:      nullableString(bead.LastHeartbeat),
		GitDiff:            nullableString(bead.GitDiff),
		Memory:             nullableString(bead.Memory),
		EstimatedMinutes:   nullablePositiveInt(bead.EstimatedMinutes),
		AcceptanceCriteria: nullableString(bead.AcceptanceCriteria),
		Dependencies:       nullableDependencies(bead.Dependencies),
		UpdatedAt:          nullableString(bead.UpdatedAt),
		ClosedAt:           nullableString(bead.ClosedAt),
		CreatedAt:          nullableString(bead.CreatedAt),
		Description:        nullableString(bead.Description),
		CloseReason:        nullableString(bead.CloseReason),
		Owner:              nullableString(bead.Owner),
		Notes:              nullableString(bead.Notes),
		Tags:               nullableStrings(bead.Tags),
		Metadata:           nullableMetadata(bead.Metadata),
		Labels:             nullableStrings(bead.Labels),
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableDependencies(value []protocol.Dependency) []protocol.Dependency {
	if value == nil {
		return []protocol.Dependency{}
	}
	return value
}

func nullableStrings(value []string) []string {
	if value == nil {
		return []string(nil)
	}
	return value
}

func nullableMetadata(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any(nil)
	}
	return value
}
