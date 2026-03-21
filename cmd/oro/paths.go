package main

import (
	"crypto/sha256"
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

// ResolveDaemonPaths returns all oro daemon state paths, respecting project scoping and env var overrides.
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
func ResolveDaemonPaths() (*Paths, error) {
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

// ResolveProjectDBPaths is an alias for ResolveDaemonPaths.
//
// Deprecated: use ResolveDaemonPaths directly. Kept for backward compatibility.
func ResolveProjectDBPaths() (*Paths, error) {
	return ResolveDaemonPaths()
}

// ProjectPaths holds all path-dependent components for a single project.
// Use ResolvePaths(repoRoot) to populate from mode detection + config.
type ProjectPaths struct {
	Mode           string // "standard" | "stealth"
	RepoRoot       string // absolute path to repo root
	BeadsDir       string // .beads/ or ~/.oro/projects/s-<hash>/beads/
	WorktreesDir   string // .worktrees/ or ~/.oro/projects/s-<hash>/worktrees/
	OroDocsDir     string // docs/ or ~/.oro/projects/s-<hash>/docs/
	QualityGate    string // scripts/quality_gate.sh or ~/.oro/projects/s-<hash>/quality_gate.sh
	OroProjectDir  string // .oro/ or ~/.oro/projects/s-<hash>/
	ClaudeMD       string // .claude/CLAUDE.md or ~/.oro/projects/s-<hash>/CLAUDE.md
	ReviewPatterns string // assets/review-patterns.md or ~/.oro/projects/s-<hash>/review-patterns.md
	ConfigYAML     string // .oro/config.yaml or ~/.oro/projects/s-<hash>/config.yaml
	WorkerProgram  string // worker-program.md or ~/.oro/projects/s-<hash>/worker-program.md
}

// ResolvePaths resolves project-level paths for the given repo root.
//
// Mode detection order:
//  1. Standard config at <repoRoot>/.oro/config.yaml → standard mode.
//  2. Stealth config at ~/.oro/projects/s-<hash>/config.yaml → stealth mode.
//  3. No config found → default to standard mode.
//
// Hash is SHA-256 of filepath.EvalSymlinks(repoRoot), truncated to 16 hex chars.
// Returns an error if ~/.oro/ is not writable when stealth mode is detected.
func ResolvePaths(repoRoot string) (ProjectPaths, error) {
	// 1. Try standard mode: .oro/config.yaml in repo root.
	stdConfig := filepath.Join(repoRoot, ".oro", "config.yaml")
	if _, err := os.Stat(stdConfig); err == nil {
		return standardProjectPaths(repoRoot), nil
	}

	// 2. Try stealth mode: ~/.oro/projects/s-<hash>/config.yaml.
	oroHome, err := resolveOroHome()
	if err != nil {
		return ProjectPaths{}, err
	}

	hash, err := projectHash(repoRoot)
	if err != nil {
		return ProjectPaths{}, err
	}

	stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)
	stealthConfig := filepath.Join(stealthDir, "config.yaml")
	if _, err := os.Stat(stealthConfig); err == nil {
		// Verify ~/.oro/ is writable before committing to stealth mode.
		if err := checkDirWritable(oroHome); err != nil {
			return ProjectPaths{}, fmt.Errorf("stealth mode requires writable ~/.oro: %w", err)
		}
		return stealthProjectPaths(repoRoot, stealthDir), nil
	}

	// 3. No config found → standard mode.
	return standardProjectPaths(repoRoot), nil
}

// standardProjectPaths returns ProjectPaths for standard (in-repo) mode.
func standardProjectPaths(repoRoot string) ProjectPaths {
	return ProjectPaths{
		Mode:           "standard",
		RepoRoot:       repoRoot,
		BeadsDir:       filepath.Join(repoRoot, ".beads"),
		WorktreesDir:   filepath.Join(repoRoot, ".worktrees"),
		OroDocsDir:     filepath.Join(repoRoot, "docs"),
		QualityGate:    filepath.Join(repoRoot, "scripts", "quality_gate.sh"),
		OroProjectDir:  filepath.Join(repoRoot, ".oro"),
		ClaudeMD:       filepath.Join(repoRoot, ".claude", "CLAUDE.md"),
		ReviewPatterns: filepath.Join(repoRoot, "assets", "review-patterns.md"),
		ConfigYAML:     filepath.Join(repoRoot, ".oro", "config.yaml"),
		WorkerProgram:  filepath.Join(repoRoot, "worker-program.md"),
	}
}

// stealthProjectPaths returns ProjectPaths for stealth (zero-footprint) mode.
func stealthProjectPaths(repoRoot, stealthDir string) ProjectPaths {
	return ProjectPaths{
		Mode:           "stealth",
		RepoRoot:       repoRoot,
		BeadsDir:       filepath.Join(stealthDir, "beads"),
		WorktreesDir:   filepath.Join(stealthDir, "worktrees"),
		OroDocsDir:     filepath.Join(stealthDir, "docs"),
		QualityGate:    filepath.Join(stealthDir, "quality_gate.sh"),
		OroProjectDir:  stealthDir,
		ClaudeMD:       filepath.Join(stealthDir, "CLAUDE.md"),
		ReviewPatterns: filepath.Join(stealthDir, "review-patterns.md"),
		ConfigYAML:     filepath.Join(stealthDir, "config.yaml"),
		WorkerProgram:  filepath.Join(stealthDir, "worker-program.md"),
	}
}

// projectHash computes a 16-hex-char project identifier from the repo root.
// Symlinks are resolved before hashing so canonical paths are stable.
func projectHash(repoRoot string) (string, error) {
	resolved, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return "", fmt.Errorf("eval symlinks %q: %w", repoRoot, err)
	}
	sum := sha256.Sum256([]byte(resolved))
	return fmt.Sprintf("%x", sum[:8]), nil // 16 hex chars
}

// checkDirWritable returns an error if dir is not writable by the current process.
func checkDirWritable(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	// Attempt to create a temp file to verify write access.
	f, err := os.CreateTemp(dir, ".oro-write-check-*")
	if err != nil {
		return fmt.Errorf("directory not writable: %w", err)
	}
	_ = f.Close()
	_ = os.Remove(f.Name()) //nolint:gosec // f.Name() is from os.CreateTemp — path is trusted
	return nil
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

		if err := os.WriteFile(db.dst, data, 0o600); err != nil { //nolint:gosec // db.dst is constructed from trusted paths
			return fmt.Errorf("write project DB %q: %w", db.dst, err)
		}
	}

	return nil
}
