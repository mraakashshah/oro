package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"
)

// Paths holds all resolved oro state file paths.
// Use ResolvePaths() to populate this struct with defaults + env overrides.
type Paths struct {
	OroHome         string // ~/.oro or ORO_HOME
	PIDPath         string // oro.pid or ORO_PID_PATH
	SocketPath      string // oro.sock or ORO_SOCKET_PATH
	StateDBPath     string // state.db or ORO_DB_PATH
	CodeIndexDBPath string // code_index.db (respects ORO_HOME)
}

// ResolvePaths returns all oro paths, respecting project scoping and env var overrides.
//
// When a project name is available (ORO_PROJECT env var or .oro/config.yaml),
// all state paths (PID, socket, DB, code index) resolve to
// ~/.oro/projects/<name>/. This enables multiple oro instances to run
// simultaneously in different projects without clashing.
//
// When no project name is found, paths fall back to ~/.oro/ (backward compat).
//
// Environment variables:
//   - ORO_HOME: base directory for all oro state (default: ~/.oro)
//   - ORO_PID_PATH: dispatcher PID file (overrides project scoping)
//   - ORO_SOCKET_PATH: dispatcher UDS socket (overrides project scoping)
//   - ORO_DB_PATH: dispatcher state database (overrides project scoping)
//   - ORO_MEMORY_DB: memory store database (default: $ORO_HOME/memories.db)
//
// OroHome always remains the global ~/.oro directory (used for worker logs,
// hooks, skills, etc.). Only per-daemon state is project-scoped.
func ResolvePaths() (*Paths, error) {
	oroHome, err := resolveOroHome()
	if err != nil {
		return nil, err
	}

	// Determine base directory for per-daemon state files.
	// With a project name, state scopes to ~/.oro/projects/<name>/.
	// Without, falls back to ~/.oro/ (backward compat).
	stateBase := oroHome
	if project := readProjectName(); project != "" {
		stateBase = filepath.Join(oroHome, "projects", project)
	}

	return &Paths{
		OroHome:         oroHome,
		PIDPath:         resolvePathWithEnv("ORO_PID_PATH", stateBase, "oro.pid"),
		SocketPath:      resolvePathWithEnv("ORO_SOCKET_PATH", stateBase, "oro.sock"),
		StateDBPath:     resolvePathWithEnv("ORO_DB_PATH", stateBase, "state.db"),
		CodeIndexDBPath: filepath.Join(stateBase, "code_index.db"),
	}, nil
}

// ResolveProjectDBPaths is an alias for ResolvePaths.
//
// Deprecated: use ResolvePaths directly. Kept for backward compatibility.
func ResolveProjectDBPaths() (*Paths, error) {
	return ResolvePaths()
}

// readProjectName returns the current project name from ORO_PROJECT env var
// or .oro/config.yaml in CWD. Returns empty string if neither is available.
func readProjectName() string {
	if v := os.Getenv("ORO_PROJECT"); v != "" {
		return v
	}

	// Try .oro/config.yaml in CWD.
	data, err := os.ReadFile(filepath.Join(".oro", "config.yaml"))
	if err != nil {
		return ""
	}

	// Minimal YAML parse: look for "project: <name>" line.
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "project:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "project:"))
			return val
		}
	}
	return ""
}

// resolveOroHome returns the oro home directory from ORO_HOME env var or ~/.oro.
func resolveOroHome() (string, error) {
	if v := os.Getenv("ORO_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, protocol.OroDir), nil
}

// resolvePathWithEnv returns the path from envKey if set, otherwise joins base + suffix.
func resolvePathWithEnv(envKey, base, suffix string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return filepath.Join(base, suffix)
}

// migrateGlobalDBs copies global ~/.oro/state.db and ~/.oro/code_index.db to
// per-project directories (~/.oro/projects/<projectName>/) on first use.
// This provides backward compatibility when transitioning from global to per-project DBs.
//
// Behavior:
// - If per-project DB already exists → no-op
// - If global DB missing → no-op (no source to copy from)
// - If copy fails → returns error, does not corrupt existing files
func migrateGlobalDBs(projectName string) error {
	oroHome, err := resolveOroHome()
	if err != nil {
		return err
	}

	projDir := filepath.Join(oroHome, "projects", projectName)

	// List of DBs to migrate: (srcPath, destPath)
	dbs := []struct {
		src string
		dst string
	}{
		{filepath.Join(oroHome, "state.db"), filepath.Join(projDir, "state.db")},
		{filepath.Join(oroHome, "code_index.db"), filepath.Join(projDir, "code_index.db")},
	}

	for _, db := range dbs {
		// Skip if per-project DB already exists
		if _, err := os.Stat(db.dst); err == nil {
			continue
		}

		// Skip if global DB doesn't exist
		if _, err := os.Stat(db.src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat global DB %q: %w", db.src, err)
		}

		// Ensure project directory exists
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			return fmt.Errorf("mkdir %q: %w", projDir, err)
		}

		// Copy global DB to project directory
		data, err := os.ReadFile(db.src) //nolint:gosec // db.src is constructed from trusted paths
		if err != nil {
			return fmt.Errorf("read global DB %q: %w", db.src, err)
		}

		if err := os.WriteFile(db.dst, data, 0o600); err != nil {
			return fmt.Errorf("write project DB %q: %w", db.dst, err)
		}
	}

	return nil
}
