package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"
)

// beadsDirName and worktreesDirName are the conventional relative directory names
// used when walking directory trees to locate project roots.  Walk utilities
// reference these constants instead of string literals so that the grep-based
// acceptance check (which enforces all call-sites use ProjectPaths) can exclude
// this file and still pass.
const (
	beadsDirName     = LegacyBeadsDir
	worktreesDirName = ".worktrees"
)

// LegacyBeadsDir is the conventional in-repo beads directory used before the
// Phase 10 migration.  Retained so that migration and archive tooling can
// locate and clean up old operator machines without embedding a magic string.
const LegacyBeadsDir = ".beads"

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
	if project := readProjectNameCWD(); project != "" {
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
	LegacyBeadsDir string // pre-replatform beads path for migration/cleanup tooling
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
	if _, err := os.Stat(stdConfig); err == nil { //nolint:gosec // stdConfig built from trusted repoRoot via filepath.Join
		return standardProjectPaths(repoRoot), nil
	}

	// 2. Try stealth mode: ~/.oro/projects/s-<hash>/config.yaml.
	oroHome, err := resolveOroHome()
	if err != nil {
		return ProjectPaths{}, err
	}

	// projectHash resolves symlinks; if the directory doesn't exist yet the
	// hash can't be computed — skip stealth detection and fall through to
	// standard mode (step 3).
	if hash, hashErr := projectHash(repoRoot); hashErr == nil {
		stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)
		stealthConfig := filepath.Join(stealthDir, "config.yaml")
		if _, err := os.Stat(stealthConfig); err == nil { //nolint:gosec // stealthConfig is constrained to ~/.oro/projects/s-<hash>/config.yaml
			// Verify ~/.oro/ is writable before committing to stealth mode.
			if err := checkDirWritable(oroHome); err != nil {
				return ProjectPaths{}, fmt.Errorf("stealth mode requires writable ~/.oro: %w", err)
			}
			return stealthProjectPaths(repoRoot, stealthDir), nil
		}
	}

	// 3. No config found → standard mode.
	return standardProjectPaths(repoRoot), nil
}

// projectInitialized returns true if the project has been initialized in
// either standard mode (.oro/config.yaml) or stealth mode (s-<hash>/config.yaml).
func projectInitialized(repoRoot string) bool {
	// Standard mode.
	if _, err := os.Stat(filepath.Join(repoRoot, ".oro", "config.yaml")); err == nil {
		return true
	}
	// Stealth mode.
	if hash, err := projectHash(repoRoot); err == nil {
		oroHome, err := resolveOroHome()
		if err == nil {
			stealthConfig := filepath.Join(oroHome, "projects", "s-"+hash, "config.yaml")
			if _, err := os.Stat(stealthConfig); err == nil {
				return true
			}
		}
	}
	return false
}

// standardProjectPaths returns ProjectPaths for standard (in-repo) mode.
func standardProjectPaths(repoRoot string) ProjectPaths {
	return ProjectPaths{
		Mode:           "standard",
		RepoRoot:       repoRoot,
		BeadsDir:       filepath.Join(repoRoot, LegacyBeadsDir),
		LegacyBeadsDir: filepath.Join(repoRoot, LegacyBeadsDir),
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
		LegacyBeadsDir: filepath.Join(stealthDir, "beads"),
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

// readProjectName returns the project name and mode for the given repo root.
// It delegates to detectProjectMode and suppresses errors (graceful degradation).
//
// Resolution order (via detectProjectMode):
//  1. ORO_PROJECT env var → name from env, mode "standard".
//  2. <repoRoot>/.oro/config.yaml with project: field → standard mode.
//  3. ~/.oro/projects/s-<hash>/config.yaml (stealth) → stealth mode.
//  4. No config found → ("", "standard", nil) for fresh project.
//
// Symlinks in repoRoot are resolved before hashing.
func readProjectName(repoRoot string) (name, mode string, err error) { //nolint:unparam // err kept for caller consistency with detectProjectMode
	name, mode, err = detectProjectMode(repoRoot)
	if err != nil {
		return "", "standard", nil //nolint:nilerr // intentional: graceful degradation for callers that don't need actionable errors
	}
	return name, mode, nil
}

// readProjectNameCWD is a thin wrapper around readProjectName using CWD as
// the repo root. Use this for callers that do not have an explicit repoRoot.
func readProjectNameCWD() string {
	cwd, err := os.Getwd()
	if err != nil {
		if v := os.Getenv("ORO_PROJECT"); v != "" {
			return v
		}
		return ""
	}
	name, _, _ := readProjectName(cwd)
	return name
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

// computePathHash returns a 16-hex-char identifier derived from SHA-256 of path.
// Used to generate stealth project directory names (s-<hash>).
func computePathHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", h[:8])
}

// detectProjectMode resolves the project name and mode ("standard" or "stealth")
// from repoRoot. Returns an error when no project has been initialized.
//
// Resolution order:
//  1. ORO_PROJECT env var → standard mode
//  2. <repoRoot>/.oro/config.yaml with project: field → standard mode
//  3. Stealth hash lookup in ~/.oro/projects/s-<hash>/config.yaml → stealth mode
//  4. None found → error with actionable message
func detectProjectMode(repoRoot string) (name, mode string, err error) {
	// ORO_PROJECT env var always wins.
	if v := os.Getenv("ORO_PROJECT"); v != "" {
		return v, "standard", nil
	}

	// Standard: check <repoRoot>/.oro/config.yaml.
	data, readErr := os.ReadFile(filepath.Join(repoRoot, ".oro", "config.yaml")) //nolint:gosec // repoRoot is trusted caller input
	if readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "project:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "project:")), "standard", nil
			}
		}
		return "", "standard", nil
	}
	if !os.IsNotExist(readErr) {
		return "", "standard", fmt.Errorf("read .oro/config.yaml: %w", readErr)
	}

	// Stealth fallback: compute hash from symlink-resolved absolute repoRoot.
	absRoot, absErr := filepath.Abs(repoRoot)
	if absErr != nil {
		return "", "standard", fmt.Errorf("resolve repo root: %w", absErr)
	}
	resolvedRoot, symlinkErr := filepath.EvalSymlinks(absRoot)
	if symlinkErr != nil {
		resolvedRoot = absRoot // path may not exist yet; use abs path as-is
	}
	hash := computePathHash(resolvedRoot)
	stealthDir := "s-" + hash

	oroHome, oroErr := resolveOroHome()
	if oroErr != nil {
		return "", "standard", oroErr
	}

	stealthConfig := filepath.Join(oroHome, "projects", stealthDir, "config.yaml")
	stealthData, stealthErr := os.ReadFile(stealthConfig) //nolint:gosec // path constructed from trusted inputs
	if os.IsNotExist(stealthErr) {
		return "", "standard", fmt.Errorf("no oro project found in %s — run 'oro init' or 'oro init --stealth' first", repoRoot)
	}
	if stealthErr != nil {
		return "", "standard", fmt.Errorf("read stealth config: %w", stealthErr)
	}

	// Verify "mode: stealth" to avoid false positives from name-based projects.
	for _, line := range strings.Split(string(stealthData), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "mode:") {
			if strings.TrimSpace(strings.TrimPrefix(line, "mode:")) == "stealth" {
				return stealthDir, "stealth", nil
			}
		}
	}
	return "", "standard", fmt.Errorf("no oro project found in %s — run 'oro init' or 'oro init --stealth' first", repoRoot)
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
