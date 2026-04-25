package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"oro/pkg/mg/app"
	"oro/pkg/mg/data"
	mgTmux "oro/pkg/mg/tmux"
)

func newMgCmd() *cobra.Command {
	var (
		path       string
		blockTypes string
		statusMode bool
	)

	cmd := &cobra.Command{
		Use:        "mg",
		Short:      "Legacy Mardi Gras TUI for beads issues",
		Long:       "Legacy BubbleTea dashboard. Prefer `oro dashboard` with `oro start --web` for the supported web UI.",
		Deprecated: "use `oro dashboard` with `oro start --web`; mg is the legacy BubbleTea UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			blockingTypes := parseBlockingTypes(blockTypes)
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			source := resolveSource(cwd, path)
			if source.Mode == data.SourceJSONL && source.Path == "" {
				return fmt.Errorf("no .beads/ directory found and bd not on PATH\n\nRun from inside a project with Beads, or specify a path:\n  oro mg --path /path/to/.beads/issues.jsonl")
			}

			issues, err := loadInitialIssues(source)
			if err != nil {
				return err
			}

			if statusMode {
				groups := data.GroupByParade(issues, blockingTypes)
				fmt.Print(mgTmux.StatusLine(groups))
				return nil
			}

			guard := app.NewOSCGuard()
			model := app.NewWithGuard(issues, source, blockingTypes, guard)
			p := tea.NewProgram(model, tea.WithFilter(guard.Filter()))
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "path to .beads/issues.jsonl file")
	cmd.Flags().StringVar(&blockTypes, "block-types", "", "comma-separated dependency types that count as blockers (default: blocks)")
	cmd.Flags().BoolVar(&statusMode, "status", false, "output tmux status line and exit")

	return cmd
}

// loadInitialIssues fetches the initial issue list from source.
func loadInitialIssues(source data.Source) ([]data.Issue, error) {
	switch source.Mode {
	case data.SourceCLI:
		active, err := data.FetchActiveIssuesCLI(source.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("loading issues via bd list: %w", err)
		}
		recentClosed, _ := data.FetchRecentClosedCLI(source.ProjectDir, 50)
		return append(active, recentClosed...), nil
	default:
		issues, skipped, err := data.LoadIssues(source.Path)
		if err != nil {
			return nil, fmt.Errorf("loading issues from %s: %w", source.Path, err)
		}
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, "Warning: skipped %d malformed line(s) in %s\n", skipped, source.Path)
		}
		return issues, nil
	}
}

// parseBlockingTypes builds the blocking types set from flag, env var, or default.
func parseBlockingTypes(flagVal string) map[string]bool {
	raw := flagVal
	if raw == "" {
		raw = os.Getenv("MG_BLOCK_TYPES")
	}
	if raw == "" {
		return data.DefaultBlockingTypes
	}
	types := make(map[string]bool)
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			types[t] = true
		}
	}
	if len(types) == 0 {
		return data.DefaultBlockingTypes
	}
	return types
}

// findBeadsFile walks up from dir looking for .beads/issues.jsonl.
func findBeadsFile(dir string) string {
	for {
		candidate := filepath.Join(dir, beadsDirName, "issues.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// findBeadsDir walks up from dir looking for a .beads/ directory.
func findBeadsDir(dir string) string {
	for {
		candidate := filepath.Join(dir, beadsDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// bdOnPath returns true if the bd command is available.
func bdOnPath() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}

// resolveSource determines how oro mg should load issues.
func resolveSource(cwd, pathFlag string) data.Source {
	if pathFlag != "" {
		return data.Source{
			Mode:       data.SourceJSONL,
			Path:       pathFlag,
			ProjectDir: filepath.Dir(filepath.Dir(pathFlag)),
			Explicit:   true,
		}
	}

	if projectDir := findBeadsDir(cwd); projectDir != "" && bdOnPath() {
		return data.NewSource(projectDir, nil)
	}

	if jsonlPath := findBeadsFile(cwd); jsonlPath != "" {
		return data.Source{
			Mode:       data.SourceJSONL,
			Path:       jsonlPath,
			ProjectDir: filepath.Dir(filepath.Dir(jsonlPath)),
		}
	}

	return data.Source{}
}
