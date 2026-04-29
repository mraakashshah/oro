package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// doltCmdConfig holds injectable dependencies for the oro dolt subcommands.
type doltCmdConfig struct {
	oroHome         string
	aliveFn         func(int) bool
	isPortUp        func(int) bool
	stopFn          func(string) error // stopDoltServer for oroHome
	force           bool
	dispatcherPIDFn func() int // returns dispatcher PID (0 = not running)
	beadsDirs       []string   // per-project .beads directories (used by teardown)
}

// newDoltCmd creates the "oro dolt" parent command with status/start/stop/setup/teardown subcommands.
func newDoltCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dolt",
		Short: "Manage the shared Dolt server",
		Long: `Manage the machine-wide shared Dolt server used by beads.

Subcommands:
  setup    Migrate per-project dolt databases to the shared server and install launchd agent
  status   Show shared server status, PID, port, and databases
  start    Start the shared server (idempotent)
  stop     Stop the shared server (requires --force if dispatcher is running)
  teardown Copy databases back to per-project dirs, stop shared server, uninstall launchd agent`,
	}

	cmd.AddCommand(newDoltSetupCmd())
	cmd.AddCommand(newDoltStatusCmd())
	cmd.AddCommand(newDoltStartCmd())
	cmd.AddCommand(newDoltStopCmd())
	cmd.AddCommand(newDoltTeardownCmd())
	cmd.AddCommand(newDoltRepairCmd())

	return cmd
}

// ---------- oro dolt setup ----------

// doltSetupConfig holds injectable dependencies for the dolt setup command.
type doltSetupConfig struct {
	oroHome         string
	homeDir         string   // user's ~ for plist installation
	beadsDirs       []string // per-project .beads directories to scan
	aliveFn         func(int) bool
	dispatcherPIDFn func() int
	startFn         func(string) (int, error) // starts shared server, writes PID/port
	generatePlistFn func(string, string, int) ([]byte, error)
	installPlistFn  func([]byte, string) error
	killOrphansFn   func([]doltProject, io.Writer) // optional; default: killOrphanDoltServers
	force           bool
}

func newDoltSetupCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Migrate per-project dolt databases to the shared server",
		Long: `Migrate per-project dolt databases to the shared ~/.oro/dolt/ directory,
update each project's metadata to point at the shared server (port 13307),
install the launchd agent so the server auto-starts, and start the server.

Aborts if the dispatcher is running. Use --force to override.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("get home dir: %w", err)
			}
			cfg := &doltSetupConfig{
				oroHome:         paths.OroHome,
				homeDir:         homeDir,
				aliveFn:         IsProcessAlive,
				force:           force,
				startFn:         startSharedDoltServer,
				generatePlistFn: generatePlist,
				installPlistFn:  installLaunchAgent,
				dispatcherPIDFn: func() int {
					pid, pidErr := ReadPIDFile(paths.PIDPath)
					if pidErr != nil {
						return 0
					}
					if !IsProcessAlive(pid) {
						return 0
					}
					return pid
				},
			}
			cfg.beadsDirs = discoverBreadsDirs(paths.OroHome)
			return runDoltSetup(cfg, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "proceed even if the dispatcher is running")
	return cmd
}

// discoverBreadsDirs scans ~/.oro/projects/ for registered projects and derives beads paths.
// For each project directory it reads project.root, resolves paths, and derives the .beads dir.
// Gracefully skips projects with missing or invalid project.root, or where the root doesn't exist.
// Falls back to an empty list when no projects are registered.
func discoverBreadsDirs(oroHome string) []string {
	projectsDir := filepath.Join(oroHome, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var dirs []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projectDir := filepath.Join(projectsDir, e.Name())
		projectRootFile := filepath.Join(projectDir, "project.root")
		rootBytes, readErr := os.ReadFile(projectRootFile) //nolint:gosec // path from trusted oroHome
		if readErr != nil {
			continue // skip if project.root missing
		}
		rootPath := strings.TrimSpace(string(rootBytes))

		// Verify project root directory exists before adding to list.
		if _, statErr := os.Stat(rootPath); statErr != nil { //nolint:gosec // rootPath from trusted project.root file
			continue // skip if project root doesn't exist
		}

		projPaths, pathErr := ResolvePaths(rootPath)
		if pathErr != nil {
			continue // skip if paths can't be resolved
		}
		if seen[projPaths.BeadsDir] {
			continue // deduplicate: multiple project entries can point to the same root
		}
		seen[projPaths.BeadsDir] = true
		dirs = append(dirs, projPaths.BeadsDir)
	}
	return dirs
}

// doltProject holds per-project information discovered during setup.
type doltProject struct {
	beadsDir string
	dbName   string
	port     int // per-project dolt server port; 0 means derive from beadsDir
}

// findDoltProjects scans beadsDirs for directories with dolt backend metadata.
func findDoltProjects(beadsDirs []string) ([]doltProject, error) {
	var projects []doltProject
	for _, beadsDir := range beadsDirs {
		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			return nil, fmt.Errorf("read dolt meta for %s: %w", beadsDir, err)
		}
		if meta == nil {
			continue
		}
		dbName := meta.DoltDatabase
		if dbName == "" {
			dbName = "beads"
		}
		projects = append(projects, doltProject{beadsDir: beadsDir, dbName: dbName, port: meta.DoltServerPort})
	}
	return projects, nil
}

// checkDBCollisions returns an error if two projects share the same database name.
func checkDBCollisions(projects []doltProject) error {
	seen := make(map[string]string) // dbName → beadsDir
	for _, p := range projects {
		if existing, ok := seen[p.dbName]; ok {
			return fmt.Errorf("database name collision: %q already used by %q and %q", p.dbName, existing, p.beadsDir)
		}
		seen[p.dbName] = p.beadsDir
	}
	return nil
}

// migrateProjects copies each project's dolt DB to the shared dir and updates its metadata.
func migrateProjects(projects []doltProject, doltDir string, w io.Writer) error {
	for _, p := range projects {
		srcDir := filepath.Join(p.beadsDir, "dolt", p.dbName)
		if err := atomicCopyDir(srcDir, doltDir, p.dbName); err != nil {
			return fmt.Errorf("copy dolt DB %q: %w", p.dbName, err)
		}
		if err := setDoltPort(p.beadsDir, SharedDoltPort); err != nil {
			return fmt.Errorf("update metadata for %s: %w", p.beadsDir, err)
		}
		fmt.Fprintf(w, "  migrated %q → shared server (port %d)\n", p.dbName, SharedDoltPort)
	}
	return nil
}

// installDoltPlist generates and installs the launchd plist for the shared server.
func installDoltPlist(cfg *doltSetupConfig, w io.Writer) error {
	doltPath, _ := exec.LookPath("dolt")
	plistBytes, err := cfg.generatePlistFn(doltPath, cfg.homeDir, SharedDoltPort)
	if err != nil {
		fmt.Fprintf(w, "warning: could not generate plist (%v); skipping launchd installation\n", err)
		return nil
	}
	if err := cfg.installPlistFn(plistBytes, cfg.homeDir); err != nil {
		return fmt.Errorf("install launch agent: %w", err)
	}
	fmt.Fprintln(w, "launch agent installed")
	return nil
}

// runDoltSetup migrates per-project dolt databases to the shared server,
// installs the launchd agent, and starts the server.
func runDoltSetup(cfg *doltSetupConfig, w io.Writer) error {
	if cfg.dispatcherPIDFn != nil {
		if dispPID := cfg.dispatcherPIDFn(); dispPID > 0 && !cfg.force {
			return fmt.Errorf("dispatcher is running (PID %d); stop it first or use --force", dispPID)
		}
	}

	projects, err := findDoltProjects(cfg.beadsDirs)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Fprintln(w, "no dolt projects found; nothing to do")
		return nil
	}

	if err := checkDBCollisions(projects); err != nil {
		return err
	}

	doltDir := filepath.Join(cfg.oroHome, "dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		return fmt.Errorf("create shared dolt dir: %w", err)
	}

	if err := migrateProjects(projects, doltDir, w); err != nil {
		return err
	}

	if cfg.killOrphansFn != nil {
		cfg.killOrphansFn(projects, w)
	} else {
		killOrphanDoltServers(projects, w)
	}

	if err := installDoltPlist(cfg, w); err != nil {
		return err
	}

	if cfg.startFn != nil {
		if _, startErr := cfg.startFn(cfg.oroHome); startErr != nil {
			return fmt.Errorf("start shared dolt server: %w", startErr)
		}
	}

	// Migration complete and shared server running: per-project registry entries
	// are now stale (all projects use SharedDoltPort). Clear them so subsequent
	// AllocatePort calls in per-project mode start from a clean slate.
	clearPortRegistry(cfg.oroHome)

	fmt.Fprintln(w, "dolt setup complete")
	return nil
}

// killOrphanDoltServers kills any per-project dolt sql-server processes that are
// still running before the shared server takes over. It is called in runDoltSetup
// after migration and before starting the shared server.
//
// Edges:
//   - port == SharedDoltPort → skip (never kill the shared server)
//   - PID file present + process alive → kill via killAndWait
//   - PID file absent + port listening → discover via lsof; warn and skip if lsof unavailable
//   - port not listening → no-op
func killOrphanDoltServers(projects []doltProject, w io.Writer) {
	killOrphanServersImpl(projects, w, IsProcessAlive, isDoltServerRunning, discoverPIDByPort, killAndWait)
}

// killOrphanServersImpl is the injectable implementation used by killOrphanDoltServers
// and directly by tests.
func killOrphanServersImpl(
	projects []doltProject,
	w io.Writer,
	aliveFn func(int) bool,
	isRunningFn func(int) bool,
	discoverFn func(int) (int, error),
	killFn func(int, string) error,
) {
	for _, p := range projects {
		port := p.port
		if port == 0 {
			port = DerivePort(p.beadsDir)
		}
		if port == SharedDoltPort {
			continue // never kill the shared server
		}

		// Strategy 1: PID file.
		pidPath := filepath.Join(p.beadsDir, "dolt-server.pid")
		if data, err := os.ReadFile(pidPath); err == nil { //nolint:gosec // beadsDir is caller-controlled
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && aliveFn(pid) {
				_ = killFn(pid, p.beadsDir)
				fmt.Fprintf(w, "  killed orphan dolt server (PID %d) for %s\n", pid, p.beadsDir)
				continue
			}
		}

		// Strategy 2: lsof fallback.
		if !isRunningFn(port) {
			continue // nothing listening on the per-project port
		}
		pid, err := discoverFn(port)
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintf(w, "warning: lsof not available; cannot kill orphan dolt server on port %d\n", port)
			continue
		}
		if err != nil {
			continue // no process found
		}
		_ = killFn(pid, p.beadsDir)
		fmt.Fprintf(w, "  killed orphan dolt server (PID %d) for %s\n", pid, p.beadsDir)
	}
}

// atomicCopyDir copies the directory at srcDir into destParent/<dbName> atomically
// using a temp directory and os.Rename. Any stale <dbName>.doltsetup-tmp directory
// in destParent is removed first (cleanup from a previous crashed run).
//
// If srcDir does not exist the copy is skipped (no source to migrate).
func atomicCopyDir(srcDir, destParent, dbName string) error {
	if _, err := os.Stat(srcDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	tmpDir := filepath.Join(destParent, dbName+".doltsetup-tmp")
	destDir := filepath.Join(destParent, dbName)

	if err := os.RemoveAll(tmpDir); err != nil {
		return fmt.Errorf("clean stale temp dir %s: %w", tmpDir, err)
	}

	if err := copyDirRecursive(srcDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("copy %s → %s: %w", srcDir, tmpDir, err)
	}

	if err := os.RemoveAll(destDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("remove existing dest %s: %w", destDir, err)
	}

	if err := os.Rename(tmpDir, destDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("rename %s → %s: %w", tmpDir, destDir, err)
	}

	return nil
}

// copyDirRecursive recursively copies src directory tree to dst.
func copyDirRecursive(src, dst string) error {
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error { //nolint:wrapcheck // inner errors wrapped
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return fmt.Errorf("rel path for %s: %w", path, relErr)
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()) //nolint:wrapcheck // os error; target path is clear
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // path is walk-derived from trusted src
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		if writeErr := os.WriteFile(target, data, info.Mode()); writeErr != nil { //nolint:gosec // target derived from trusted dst
			return fmt.Errorf("write %s: %w", target, writeErr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("walk %s: %w", src, err)
	}
	return nil
}

// ---------- oro dolt teardown ----------

func newDoltTeardownCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Reverse setup: restore per-project databases, stop shared server, uninstall launchd agent",
		Long: `Reverse of oro dolt setup: stop the shared Dolt server, uninstall the
launchd launch agent, and copy databases back to per-project .beads/dolt/
directories. Each project's metadata port is restored to its derived
per-project value.

Skips copy-back for a project whose .beads/dolt/<dbName> already exists
(emits a warning instead). Aborts if the dispatcher is running unless
--force is specified.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("get home dir: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:   paths.OroHome,
				aliveFn:   IsProcessAlive,
				isPortUp:  isDoltServerRunning,
				force:     force,
				stopFn:    stopDoltServer,
				beadsDirs: discoverBreadsDirs(paths.OroHome),
				dispatcherPIDFn: func() int {
					pid, pidErr := ReadPIDFile(paths.PIDPath)
					if pidErr != nil {
						return 0
					}
					if !IsProcessAlive(pid) {
						return 0
					}
					return pid
				},
			}
			return runDoltTeardown(cfg, homeDir, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "stop even if the dispatcher is running")
	return cmd
}

// runDoltTeardown stops the shared server, uninstalls the launchd agent,
// and copies databases back to per-project directories.
func runDoltTeardown(cfg *doltCmdConfig, homeDir string, w io.Writer) error {
	if err := runDoltStop(cfg, w); err != nil {
		return err
	}
	if err := uninstallLaunchAgent(homeDir); err != nil {
		return fmt.Errorf("uninstall launch agent: %w", err)
	}
	if err := restorePerProjectDBs(cfg, w); err != nil {
		return err
	}
	fmt.Fprintln(w, "dolt teardown complete")
	return nil
}

// restorePerProjectDBs copies each project's dolt database from the shared
// ~/.oro/dolt/<dbName> directory back to <beadsDir>/dolt/<dbName> and resets
// the per-project metadata port via AllocatePort.
//
// Edges:
//   - <beadsDir>/dolt/<dbName> already exists → skip copy, emit warning.
//   - No dolt metadata for a beadsDir → skip that project.
func restorePerProjectDBs(cfg *doltCmdConfig, w io.Writer) error {
	// Clear all per-project allocations first so each project gets a fresh
	// collision-free port assignment via AllocatePort below.
	clearPortRegistry(cfg.oroHome)

	for _, beadsDir := range cfg.beadsDirs {
		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			return fmt.Errorf("read dolt meta for %s: %w", beadsDir, err)
		}
		if meta == nil {
			continue
		}

		dbName := meta.DoltDatabase
		if dbName == "" {
			dbName = "beads"
		}

		srcDir := filepath.Join(cfg.oroHome, "dolt", dbName)
		destLocal := filepath.Join(beadsDir, "dolt", dbName)

		if _, statErr := os.Stat(destLocal); statErr == nil {
			fmt.Fprintf(w, "warning: %s already exists; skipping copy back for %q\n", destLocal, dbName)
			continue
		}

		destParent := filepath.Join(beadsDir, "dolt")
		if err := atomicCopyDir(srcDir, destParent, dbName); err != nil {
			return fmt.Errorf("copy DB %q back to %s: %w", dbName, beadsDir, err)
		}

		projectName := filepath.Base(filepath.Dir(beadsDir))
		perProjectPort, allocErr := AllocatePort(beadsDir, projectName, cfg.oroHome)
		if allocErr != nil {
			perProjectPort = DerivePort(beadsDir)
		}
		if err := setDoltPort(beadsDir, perProjectPort); err != nil {
			return fmt.Errorf("restore per-project port for %s: %w", beadsDir, err)
		}

		fmt.Fprintf(w, "  restored %q → %s (port %d)\n", dbName, beadsDir, perProjectPort)
	}
	return nil
}

// ---------- oro dolt status ----------

func newDoltStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show shared Dolt server status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:  paths.OroHome,
				aliveFn:  IsProcessAlive,
				isPortUp: isDoltServerRunning,
			}
			return runDoltStatus(cfg, cmd.OutOrStdout())
		},
	}
}

// runDoltStatus prints the shared server status to w.
func runDoltStatus(cfg *doltCmdConfig, w io.Writer) error {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	portPath := filepath.Join(cfg.oroHome, "dolt-server.port")

	pid, port, running := readSharedServerState(cfg, pidPath, portPath)

	if !running {
		fmt.Fprintln(w, "shared dolt server: stopped")
		return nil
	}

	fmt.Fprintf(w, "shared dolt server: running (PID %d, port %d)\n", pid, port)

	// List databases in the data directory.
	doltDir := filepath.Join(cfg.oroHome, "dolt")
	dbs := listDatabases(doltDir)
	if len(dbs) > 0 {
		fmt.Fprintln(w, "databases:")
		for _, db := range dbs {
			fmt.Fprintf(w, "  - %s\n", db)
		}
	}

	return nil
}

// readSharedServerState reads PID and port files, then checks liveness.
// Falls back to port-based detection when the PID file is absent (e.g. launchd-managed server).
// Returns (pid, port, running).
func readSharedServerState(cfg *doltCmdConfig, pidPath, portPath string) (pid, port int, running bool) {
	pidData, err := os.ReadFile(pidPath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		// No PID file — check if the port is active (covers launchd-managed servers).
		if cfg.isPortUp != nil && cfg.isPortUp(SharedDoltPort) {
			discoveredPID, _ := discoverPIDByPort(SharedDoltPort) // best-effort; 0 if lsof unavailable
			return discoveredPID, SharedDoltPort, true
		}
		return 0, 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, 0, false
	}

	portData, err := os.ReadFile(portPath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		return pid, SharedDoltPort, cfg.aliveFn(pid)
	}
	port, err = strconv.Atoi(strings.TrimSpace(string(portData)))
	if err != nil {
		port = SharedDoltPort
	}

	running = cfg.aliveFn(pid)
	return pid, port, running
}

// listDatabases returns directory names under the dolt data directory.
func listDatabases(doltDir string) []string {
	entries, err := os.ReadDir(doltDir)
	if err != nil {
		return nil
	}
	var dbs []string
	for _, e := range entries {
		if e.IsDir() {
			dbs = append(dbs, e.Name())
		}
	}
	return dbs
}

// ---------- oro dolt start ----------

func newDoltStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the shared Dolt server (idempotent)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			pid, err := ensureSharedDoltRunningFn(paths.OroHome)
			if err != nil {
				if errors.Is(err, exec.ErrNotFound) {
					return fmt.Errorf("dolt not found in PATH: %w", err)
				}
				return fmt.Errorf("start shared dolt server: %w", err)
			}

			if pid == 0 {
				// Already running — adopted existing server.
				fmt.Fprintln(cmd.OutOrStdout(), "shared dolt server already running")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "shared dolt server started (PID %d, port %d)\n", pid, SharedDoltPort)
			return nil
		},
	}
}

// ---------- oro dolt stop ----------

func newDoltStopCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the shared Dolt server",
		Long: `Stop the shared Dolt server.

Refuses to stop when the dispatcher is running unless --force is specified,
because running workers depend on the server for beads persistence.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:  paths.OroHome,
				aliveFn:  IsProcessAlive,
				isPortUp: isDoltServerRunning,
				force:    force,
				stopFn:   stopDoltServer,
				dispatcherPIDFn: func() int {
					pid, err := ReadPIDFile(paths.PIDPath)
					if err != nil {
						return 0
					}
					if !IsProcessAlive(pid) {
						return 0
					}
					return pid
				},
			}
			return runDoltStop(cfg, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "stop even if the dispatcher is running")
	return cmd
}

// runDoltStop stops the shared server with dispatcher guard.
func runDoltStop(cfg *doltCmdConfig, w io.Writer) error {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	portPath := filepath.Join(cfg.oroHome, "dolt-server.port")

	_, _, running := readSharedServerState(cfg, pidPath, portPath)
	if !running {
		// Also check port directly in case PID file is missing.
		if !cfg.isPortUp(SharedDoltPort) {
			fmt.Fprintln(w, "shared dolt server is not running")
			return nil
		}
	}

	// Guard: refuse if dispatcher is running unless --force.
	if cfg.dispatcherPIDFn != nil {
		dispPID := cfg.dispatcherPIDFn()
		if dispPID > 0 && !cfg.force {
			return fmt.Errorf("dispatcher is running (PID %d); use --force to stop the dolt server anyway", dispPID)
		}
		if dispPID > 0 {
			fmt.Fprintf(w, "warning: dispatcher is running (PID %d), stopping dolt server anyway (--force)\n", dispPID)
		}
	}

	if err := cfg.stopFn(cfg.oroHome); err != nil {
		return fmt.Errorf("stop shared dolt server: %w", err)
	}

	fmt.Fprintln(w, "shared dolt server stopped")
	return nil
}
