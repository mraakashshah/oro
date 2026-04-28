package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newBeadCmd() *cobra.Command {
	return newBeadCmdWithStore(nil)
}

func newBeadCmdWithStore(store beadstore.Store) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "bead",
		Short: "Manage native Oro beads",
		Long:  "Manage native Oro beads.",
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON output")

	cmd.AddCommand(
		newBeadReadyCmd(store),
		newBeadListCmd(store),
		newBeadShowCmd(store),
		newBeadCreateCmd(store),
		newBeadUpdateCmd(store),
		newBeadCloseCmd(store),
		newBeadBlockedCmd(store),
		newBeadClosedCmd(store),
		newBeadDepCmd(store),
		newBeadExportCmd(store),
		newBeadMigrateFromDoltCmd(store),
	)

	return cmd
}

func newBeadReadyCmd(store beadstore.Store) *cobra.Command {
	return newBeadListLikeCmd(store, "ready", "List unblocked open beads", cobra.NoArgs, func(ctx context.Context, s beadstore.Store, _ *cobra.Command) ([]protocol.Bead, error) {
		return s.Ready(ctx)
	})
}

func newBeadListCmd(store beadstore.Store) *cobra.Command {
	cmd := newBeadListLikeCmd(store, "list", "List beads with optional filters", cobra.NoArgs, listBeadsForCmd)
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
				return err
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

func newBeadCreateCmd(store beadstore.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bead",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			params := beadstore.CreateParams{
				Title:              mustStringFlag(cmd, "title"),
				Type:               mustStringFlag(cmd, "type"),
				Priority:           mustIntFlag(cmd, "priority"),
				ParentID:           mustStringFlag(cmd, "parent"),
				Description:        mustStringFlag(cmd, "description"),
				AcceptanceCriteria: acceptance,
				EstimatedMinutes:   mustIntFlag(cmd, "estimate"),
				ID:                 mustStringFlag(cmd, "id"),
				Tags:               mustStringArrayFlag(cmd, "tag"),
			}
			bead, err := s.Create(cmd.Context(), params)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "create", err)
			}

			if isJSONOutput(cmd) {
				return writeBeadJSON(cmd, *bead)
			}
			fmt.Fprintln(cmd.OutOrStdout(), bead.ID)
			return nil
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
	return cmd
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

	addCmd := newBeadStubCmd(store, "add <bead-id> <depends-on-id>", "Add a dependency", cobra.ExactArgs(2))
	addCmd.Flags().String("type", "blocks", "dependency type")

	cmd.AddCommand(
		addCmd,
		newBeadStubCmd(store, "rm <bead-id> <depends-on-id>", "Remove a dependency", cobra.ExactArgs(2)),
		newBeadStubCmd(store, "list <bead-id>", "List dependencies for a bead", cobra.ExactArgs(1)),
	)

	return cmd
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
				return os.WriteFile(outPath, data, 0o600)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().String("out", "", "output path")
	cmd.Flags().String("format", "jsonl", "output format: jsonl or json")
	return cmd
}

func newBeadStubCmd(store beadstore.Store, use, short string, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = store
			err := fmt.Errorf("%s is not implemented yet", cmd.CommandPath())
			return writeBeadCommandErrorIfJSON(cmd, "unsupported", err)
		},
	}
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
			for _, bead := range beads {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tP%d\t%s\n", bead.ID, bead.Status, bead.Priority, bead.Title)
			}
			return nil
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

	var (
		beads []protocol.Bead
		err   error
	)
	switch status {
	case "", "open":
		beads, err = s.Ready(ctx)
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
		return nil, err
	}
	return applyBeadListFilters(beads, status, parent, limit), nil
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
	s, err := beadstore.OpenSQLiteStore(context.Background(), paths.StateDBPath)
	if err != nil {
		return nil, fmt.Errorf("open bead store: %w", err)
	}
	return s, nil
}

type beadJSON struct {
	ID                 string                `json:"id"`
	Title              string                `json:"title"`
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
	return enc.Encode(beadJSONFromProtocol(bead))
}

func writeBeadsJSON(cmd *cobra.Command, beads []protocol.Bead) error {
	out := make([]beadJSON, 0, len(beads))
	for _, bead := range beads {
		out = append(out, beadJSONFromProtocol(bead))
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func writeBeadCommandErrorIfJSON(cmd *cobra.Command, code string, err error) error {
	if !isJSONOutput(cmd) {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(beadCommandErrorJSON{
		OK:      false,
		Error:   code,
		Message: err.Error(),
		Command: cmd.CommandPath(),
	})
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

func isJSONOutput(cmd *cobra.Command) bool {
	jsonOutput, err := cmd.Flags().GetBool("json")
	return err == nil && jsonOutput
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
