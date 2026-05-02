package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"oro/pkg/beadstore"
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
			if source.Err == nil && source.Mode == data.SourceJSONL && source.Path == "" {
				return fmt.Errorf("no native bead store or JSONL snapshot found\n\nRun from inside an Oro project with a state database, or specify a JSONL path:\n  oro mg --path /path/to/.beads/issues.jsonl")
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
	if source.Err != nil {
		return nil, source.Err
	}
	switch source.Mode {
	case data.SourceCLI:
		active, err := data.FetchActiveIssues(source.Store)
		if err != nil {
			return nil, fmt.Errorf("loading issues via bead store: %w", err)
		}
		recentClosed, _ := data.FetchRecentClosed(source.Store, 50)
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

	if projectDir := findProjectDir(cwd); projectDir != "" {
		jsonlPath := findBeadsFile(cwd)
		store, err := openMgStore(projectDir)
		if err == nil {
			return data.NewSource(store, projectDir)
		}
		if jsonlPath != "" {
			return data.Source{
				Mode:       data.SourceJSONL,
				Path:       jsonlPath,
				ProjectDir: filepath.Dir(filepath.Dir(jsonlPath)),
			}
		}
		return data.Source{Err: fmt.Errorf("open mg bead store: %w", err), ProjectDir: projectDir}
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

func findProjectDir(dir string) string {
	if projectDir := findBeadsDir(dir); projectDir != "" {
		return projectDir
	}
	for {
		if projectInitialized(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func openMgStore(projectDir string) (beadstore.Store, error) {
	oroHome, err := resolveOroHome()
	if err != nil {
		return nil, fmt.Errorf("resolve oro home: %w", err)
	}
	stateBase := oroHome
	if os.Getenv("ORO_DB_PATH") == "" {
		project, err := readProjectNameForSource(projectDir, oroHome)
		if err != nil {
			return nil, fmt.Errorf("read project name: %w", err)
		}
		if project != "" {
			stateBase = filepath.Join(oroHome, "projects", project)
		}
	}
	stateDBPath := resolvePathWithEnv("ORO_DB_PATH", stateBase, "state.db")
	if _, err := os.Stat(stateDBPath); err != nil {
		return nil, fmt.Errorf("stat state db %s: %w", stateDBPath, err)
	}
	store, err := beadstore.OpenSQLiteStore(context.Background(), stateDBPath)
	if err != nil {
		return nil, fmt.Errorf("open mg state db %s: %w", stateDBPath, err)
	}
	return store, nil
}

func readProjectNameForSource(projectDir, oroHome string) (string, error) {
	if v := os.Getenv("ORO_PROJECT"); v != "" {
		return v, nil
	}
	configPath := filepath.Join(projectDir, ".oro", "config.yaml")
	configData, err := os.ReadFile(configPath) //nolint:gosec // projectDir is discovered from cwd walk.
	if err == nil {
		for _, line := range strings.Split(string(configData), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "project:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "project:")), nil
			}
		}
		return "", nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read project config %s: %w", configPath, err)
	}

	hash, err := projectHash(projectDir)
	if err != nil {
		return "", err
	}
	stealthConfig := filepath.Join(oroHome, "projects", "s-"+hash, "config.yaml")
	if _, err := os.Stat(stealthConfig); err == nil {
		return "s-" + hash, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat stealth config %s: %w", stealthConfig, err)
	}
	return "", nil
}
