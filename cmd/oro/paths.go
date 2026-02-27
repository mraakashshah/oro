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
	MemoryDBPath    string // memories.db or ORO_MEMORY_DB
	CodeIndexDBPath string // code_index.db (respects ORO_HOME)
}

// ResolvePaths returns all oro paths, respecting env var overrides.
// Environment variables:
//   - ORO_HOME: base directory for all oro state (default: ~/.oro)
//   - ORO_PID_PATH: dispatcher PID file (default: $ORO_HOME/oro.pid)
//   - ORO_SOCKET_PATH: dispatcher UDS socket (default: $ORO_HOME/oro.sock)
//   - ORO_DB_PATH: dispatcher state database (default: $ORO_HOME/state.db)
//   - ORO_MEMORY_DB: memory store database (default: $ORO_HOME/memories.db)
//
// If ORO_HOME is set, it becomes the base for all default paths.
// Specific env vars (ORO_PID_PATH, etc.) override both the default and ORO_HOME base.
func ResolvePaths() (*Paths, error) {
	// 1. Resolve OroHome (ORO_HOME or ~/.oro).
	oroHome, err := resolveOroHome()
	if err != nil {
		return nil, err
	}

	// 2. Resolve each path with env overrides.
	paths := &Paths{
		OroHome:         oroHome,
		PIDPath:         resolvePathWithEnv("ORO_PID_PATH", oroHome, "oro.pid"),
		SocketPath:      resolvePathWithEnv("ORO_SOCKET_PATH", oroHome, "oro.sock"),
		StateDBPath:     resolvePathWithEnv("ORO_DB_PATH", oroHome, "state.db"),
		MemoryDBPath:    resolvePathWithEnv("ORO_MEMORY_DB", oroHome, "memories.db"),
		CodeIndexDBPath: filepath.Join(oroHome, "code_index.db"),
	}

	return paths, nil
}

// ResolveProjectDBPaths returns paths with DB paths scoped to the current project.
// Resolution order for project name:
//  1. ORO_PROJECT env var (set by dispatcher for workers, and by oro work)
//  2. .oro/config.yaml "project" field in CWD
//  3. Empty string → fall back to global ~/.oro/ paths
//
// When a project name is found, StateDBPath and CodeIndexDBPath resolve to
// ~/.oro/projects/<name>/. PIDPath and SocketPath remain global.
func ResolveProjectDBPaths() (*Paths, error) {
	base, err := ResolvePaths()
	if err != nil {
		return nil, err
	}

	project := readProjectName()
	if project == "" {
		return base, nil
	}

	projDir := filepath.Join(base.OroHome, "projects", project)
	base.StateDBPath = filepath.Join(projDir, "state.db")
	base.CodeIndexDBPath = filepath.Join(projDir, "code_index.db")

	return base, nil
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
