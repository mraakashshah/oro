package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oro/pkg/codesearch"
	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// DaemonSpawner abstracts spawning the daemon subprocess for testability.
type DaemonSpawner interface {
	SpawnDaemon(pidPath string, workers int) (pid int, err error)
}

// ExecDaemonSpawner spawns a real child process running `oro start --daemon-only`.
// Optional timeout fields are forwarded as CLI flags to the child process.
type ExecDaemonSpawner struct {
	ProgressTimeout time.Duration
	ReviewTimeout   time.Duration
}

// buildArgs constructs the CLI arguments for the daemon child process.
func (e *ExecDaemonSpawner) buildArgs(workers int) []string {
	args := []string{"start", "--daemon-only", "--workers", strconv.Itoa(workers)}
	if e.ProgressTimeout > 0 {
		args = append(args, "--progress-timeout="+e.ProgressTimeout.String())
	}
	if e.ReviewTimeout > 0 {
		args = append(args, "--review-timeout="+e.ReviewTimeout.String())
	}
	return args
}

// SpawnDaemon forks a child process running the current binary with --daemon-only.
// The child is placed in its own session (Setsid) so it survives parent exit
// without receiving SIGHUP from the parent's process group.
func (e *ExecDaemonSpawner) SpawnDaemon(pidPath string, workers int) (int, error) {
	// Use exec.Command (not CommandContext) — the daemon is a long-lived child
	// that must survive parent exit. CommandContext starts an internal goroutine
	// tied to the parent process lifecycle; plain Command avoids this entirely.
	child := exec.Command(os.Args[0], e.buildArgs(workers)...) //nolint:gosec,noctx // intentionally re-executing self; no context — daemon must outlive parent

	// Redirect daemon stdout/stderr to a log file. Inheriting the parent's
	// stdout/stderr causes SIGPIPE when the parent exits (broken pipe),
	// silently killing the daemon.
	logPath := daemonLogPath(readProjectName())
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // log path is deterministic
	if err != nil {
		return 0, fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	child.Stdout = logFile
	child.Stderr = logFile

	child.Env = cleanEnvForDaemon(os.Environ())
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("spawn daemon: %w", err)
	}
	// logFile fd is inherited by the child; parent can close its copy.
	_ = logFile.Close()
	return child.Process.Pid, nil
}

// cleanEnvForDaemon returns a copy of env with vars that should not leak
// into the daemon subprocess removed. CLAUDECODE causes nested Claude Code
// session detection which blocks workers from spawning claude -p.
func cleanEnvForDaemon(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			continue
		}
		cleaned = append(cleaned, e)
	}
	return cleaned
}

// socketPollTimeout is the maximum time to wait for the dispatcher socket.
// 15s allows for DB migrations, code indexing, and schema init on first run.
const socketPollTimeout = 15 * time.Second

// socketPollInterval is how often to check for the socket file.
const socketPollInterval = 50 * time.Millisecond

// isDetached returns true when oro start should skip interactive attach.
// This happens when the --detach flag is set or stdin is not a terminal.
func isDetached(flag bool) bool {
	return flag || !isatty.IsTerminal(os.Stdin.Fd())
}

// runFullStart implements the non-daemon start flow:
// 1. Spawn daemon subprocess
// 2. Wait for socket file to appear
// 3. Create tmux session with both beacons
// 4. Print status
// 5. Attach interactively (or print instructions if detached)
// waitForSocket polls sockPath until it appears or socketTimeout elapses.
// It manages the spinner on the startup log.
func pollForSocket(log *startupLog, sockPath string, socketTimeout time.Duration) error {
	var stopSpinner func()
	if log != nil {
		stopSpinner = log.StartSpinner("Waiting for dispatcher socket...")
	}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(socketTimeout)
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(context.Background(), "unix", sockPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(socketPollInterval)
	}
	// Final check: must be connectable.
	conn, err := dialer.DialContext(context.Background(), "unix", sockPath)
	if err != nil {
		if stopSpinner != nil {
			stopSpinner()
		}
		return fmt.Errorf("dispatcher socket not ready at %s: %w", sockPath, err)
	}
	_ = conn.Close()
	if stopSpinner != nil {
		stopSpinner()
	}
	if log != nil {
		log.Step("Dispatcher socket ready")
	}
	return nil
}

func runFullStart(w io.Writer, workers int, model, project string, spawner DaemonSpawner, tmuxRunner CmdRunner, killFn func(int) error, socketTimeout time.Duration, sleeper func(time.Duration), beaconTimeout time.Duration, detach bool, doltStartFn func() (int, error)) error {
	// Initialize startup logger (TTY detection for spinner vs static output)
	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	log := newStartupLog(w, isTTY)

	paths, err := ResolvePaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	pidPath := paths.PIDPath
	sockPath := paths.SocketPath

	log.Step("Preflight checks passed")

	// 0. Start dolt server before daemon (dolt must be up before dispatcher connects).
	// doltCleanup is a no-op unless dolt was successfully started.
	doltCleanup, err := startDoltIfNeeded(doltStartFn)
	if err != nil {
		return err
	}

	// 1. Spawn the daemon subprocess.
	pid, err := spawner.SpawnDaemon(pidPath, workers)
	if err != nil {
		doltCleanup()
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// cleanupOrphans kills the daemon and stops dolt on error after spawn.
	cleanupOrphans := func() {
		if killErr := killFn(pid); killErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to kill orphaned daemon (PID %d): %v\n", pid, killErr)
		}
		doltCleanup()
	}

	log.Step(fmt.Sprintf("Daemon started (PID %d)", pid))

	// 2. Wait for the dispatcher socket to appear.
	if err := pollForSocket(log, sockPath, socketTimeout); err != nil {
		cleanupOrphans()
		return err
	}

	// 2b. Send start directive so dispatcher transitions from Inert to Running.
	if err := sendStartDirective(sockPath); err != nil {
		cleanupOrphans()
		return fmt.Errorf("send start directive: %w", err)
	}

	// 3. Create tmux session with short nudges (full role context injected by SessionStart hook).
	sess := &TmuxSession{Name: TmuxSessionName(project), Project: project, Runner: tmuxRunner, Sleeper: sleeper, BeaconTimeout: beaconTimeout}
	if err := sess.Create(ArchitectNudge(), ManagerNudge()); err != nil {
		cleanupOrphans()
		return fmt.Errorf("create tmux session: %w", err)
	}

	log.Step("Tmux session created")
	log.Step("Beacon verified")
	fmt.Fprintf(w, "oro swarm started (PID %d, workers=%d, model=%s)\n", pid, workers, model)

	return attachOrDetach(w, sess, detach)
}

// attachOrDetach prints status and either attaches to the tmux session
// interactively or prints detach instructions.
func attachOrDetach(w io.Writer, sess *TmuxSession, detach bool) error {
	if detach {
		fmt.Fprintln(w, "detached — attach with: oro attach")
		return nil
	}
	fmt.Fprintln(w, "ctrl-b 0/1: switch panes | ctrl-b d: detach | oro stop: quit")
	if err := sess.AttachInteractive(); err != nil {
		return fmt.Errorf("attach to tmux session: %w", err)
	}
	return nil
}

// preflightAndCheckRunning runs preflight checks, bootstraps the oro dir,
// and checks if the daemon is already running. Returns the pidPath on success,
// or "" if the daemon is already running (caller should return nil).
func preflightAndCheckRunning(w io.Writer) (pidPath string, err error) {
	// Clear CLAUDECODE early — it leaks from Claude Code's Bash tool
	// and blocks nested claude sessions in tmux panes and workers.
	os.Unsetenv("CLAUDECODE")

	if err := runPreflightChecks(); err != nil {
		return "", fmt.Errorf("preflight checks failed: %w", err)
	}

	// Auto-run oro init if .oro/config.yaml is missing (project not initialized).
	if _, statErr := os.Stat(filepath.Join(".oro", "config.yaml")); os.IsNotExist(statErr) {
		fmt.Fprintf(w, "project not initialized — running oro init...\n")
		if initErr := runInit(w, false, false, ".", ""); initErr != nil {
			return "", fmt.Errorf("auto-init failed: %w — run 'oro init' manually", initErr)
		}
	}

	paths, err := ResolvePaths()
	if err != nil {
		return "", fmt.Errorf("resolve paths: %w", err)
	}

	if err := bootstrapOroDir(paths.OroHome); err != nil {
		return "", fmt.Errorf("bootstrap oro dir: %w", err)
	}

	// Re-extract assets if the binary's embedded version differs from the on-disk stamp.
	reExtracted, err := checkAssetVersion(paths.OroHome, EmbeddedAssets)
	if err != nil {
		return "", err
	}
	if reExtracted {
		regenerateProjectSettings(w, paths.OroHome, readProjectName())
	}

	// Warn if oro-search-hook binary is absent — do NOT build it here since
	// oro start may run outside the repo (go-install users lack the source tree).
	searchHookBin := filepath.Join(paths.OroHome, "hooks", "oro-search-hook")
	warnIfSearchHookMissing(w, searchHookBin)

	// Warn about quality_gate.sh issues
	repoRoot, err := os.Getwd()
	if err == nil {
		warnIfQualityGateMissing(w, repoRoot)
		warnIfQualityGateUntracked(w, repoRoot)
		warnIfEpicCNotDeployed(w, repoRoot)
	}

	pidPath = paths.PIDPath
	sockPath := paths.SocketPath

	status, pid, err := DaemonStatus(pidPath, sockPath)
	if err != nil {
		return "", fmt.Errorf("get daemon status: %w", err)
	}

	switch status {
	case StatusRunning:
		fmt.Fprintf(w, "dispatcher already running (PID %d)\n", pid)
		return "", nil
	case StatusStale:
		_ = RemovePIDFile(pidPath)
		_ = os.Remove(sockPath)
	case StatusStopped:
		// Good to go.
	}

	return pidPath, nil
}

// reconnectTmux ensures the tmux session is healthy when the daemon is already
// running. If the session is unhealthy (Claude crashed back to shell), it kills
// and recreates it. With detach=true, prints attach instructions instead of
// attaching interactively.
func reconnectTmux(w io.Writer, runner CmdRunner, project string, detach bool, sleeper func(time.Duration), beaconTimeout time.Duration) error {
	sess := &TmuxSession{Name: TmuxSessionName(project), Project: project, Runner: runner, Sleeper: sleeper, BeaconTimeout: beaconTimeout}

	wasHealthy := sess.Exists() && sess.isHealthy()
	if !wasHealthy {
		fmt.Fprintf(w, "session unhealthy — recreating tmux panes\n")
	}

	if err := sess.Create(ArchitectNudge(), ManagerNudge()); err != nil {
		return fmt.Errorf("recreate tmux session: %w", err)
	}

	if detach {
		fmt.Fprintln(w, "detached — attach with: oro attach")
		return nil
	}
	fmt.Fprintln(w, "ctrl-b 0/1: switch panes | ctrl-b d: detach | oro stop: quit")
	return sess.AttachInteractive()
}

// regenerateProjectSettings writes an updated settings.json for the current project
// when assets have been re-extracted on version bump. No-op when projectName is empty.
func regenerateProjectSettings(w io.Writer, oroHome, projectName string) {
	if projectName == "" {
		return
	}
	projectDir := filepath.Join(oroHome, "projects", projectName)
	if err := os.MkdirAll(projectDir, 0o755); err != nil { //nolint:gosec // project dir needs to be readable
		fmt.Fprintf(w, "warning: could not create project dir for settings update: %v\n", err)
		return
	}
	data, err := generateSettings("$HOME/.oro")
	if err != nil {
		fmt.Fprintf(w, "warning: could not generate settings: %v\n", err)
		return
	}
	settingsPath := filepath.Join(projectDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil { //nolint:gosec // settings file needs to be readable
		fmt.Fprintf(w, "warning: could not write settings.json: %v\n", err)
	}
}

// newStartCmd creates the "oro start" subcommand.
func newStartCmd() *cobra.Command {
	var (
		workers         int
		daemonOnly      bool
		detach          bool
		model           string
		progressTimeout time.Duration
		reviewTimeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := preflightAndCheckRunning(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if pidPath == "" {
				// Daemon running — ensure tmux session is healthy and reconnect.
				if daemonOnly {
					return nil
				}
				project, _ := readProjectConfig(".")
				return reconnectTmux(cmd.OutOrStdout(), &ExecRunner{}, project,
					isDetached(detach), nil, 0)
			}
			if daemonOnly {
				return runDaemonOnly(cmd, pidPath, workers, progressTimeout, reviewTimeout)
			}
			return startFreshSwarm(cmd.OutOrStdout(), workers, model, detach, progressTimeout, reviewTimeout)
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")
	cmd.Flags().BoolVarP(&detach, "detach", "D", false, "start in detached mode (don't attach to tmux session)")
	cmd.Flags().DurationVar(&progressTimeout, "progress-timeout", 0, "max time without worker progress before STUCK_WORKER (default 10m)")
	cmd.Flags().DurationVar(&reviewTimeout, "review-timeout", 0, "max time a reviewing worker can stall (default 15m)")

	return cmd
}

// startDoltIfNeeded starts the dolt server when doltStartFn is non-nil and
// returns a cleanup function that stops dolt on error. Returns a no-op cleanup
// and nil error when doltStartFn is nil (non-dolt project).
func startDoltIfNeeded(doltStartFn func() (int, error)) (cleanup func(), err error) {
	noop := func() {}
	if doltStartFn == nil {
		return noop, nil
	}
	if _, err := doltStartFn(); err != nil {
		return noop, fmt.Errorf("start dolt: %w", err)
	}
	// Dolt persists across sessions — never stop it on cleanup.
	return noop, nil
}

// makeDoltLifecycle reads .beads/metadata.json from workDir and returns start/stop
// functions for the dolt server if the backend is "dolt". Returns (nil, nil) for
// non-dolt projects or when the metadata file is missing or unreadable.
func makeDoltLifecycle(workDir string) (func() (int, error), func() error) { //nolint:gocritic // named results hurt readability here
	beadsDir := filepath.Join(workDir, ".beads")
	meta, err := readDoltMeta(beadsDir)
	if err != nil || meta == nil {
		return nil, nil
	}
	port := meta.DoltServerPort
	if port == 0 {
		port = DerivePort(beadsDir)
	}
	return func() (int, error) { return startDoltServer(beadsDir, port) },
		func() error { return stopDoltServer(beadsDir) }
}

// startFreshSwarm sets up project env vars and launches the full swarm (daemon + tmux).
func startFreshSwarm(w io.Writer, workers int, model string, detach bool, progressTimeout, reviewTimeout time.Duration) error {
	project, err := readProjectConfig(".")
	if err != nil {
		return fmt.Errorf("read project config: %w", err)
	}
	if project != "" {
		if err := os.Setenv("ORO_PROJECT", project); err != nil {
			return fmt.Errorf("set ORO_PROJECT: %w", err)
		}
	}
	oroHome, err := resolveOroHome()
	if err != nil {
		return err
	}
	if err := os.Setenv("ORO_HOME", oroHome); err != nil {
		return fmt.Errorf("set ORO_HOME: %w", err)
	}
	doltStart, _ := makeDoltLifecycle(".")
	return runFullStart(w, workers, model, project,
		&ExecDaemonSpawner{ProgressTimeout: progressTimeout, ReviewTimeout: reviewTimeout},
		&ExecRunner{},
		func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) },
		socketPollTimeout, nil, 0, isDetached(detach),
		doltStart)
}

// cleanStaleWorkerLogs deletes worker log directories older than maxAge.
// Skips non-directories and tolerates individual removal failures.
func cleanStaleWorkerLogs(oroHome string, maxAge time.Duration) { //nolint:unparam // maxAge parameterized for testability
	dir := filepath.Join(oroHome, "workers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir doesn't exist or unreadable — nothing to clean
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// runDaemonOnly runs the dispatcher in the foreground (used for testing/CI).
func runDaemonOnly(cmd *cobra.Command, pidPath string, workers int, progressTimeout, reviewTimeout time.Duration) error {
	fmt.Fprintf(cmd.OutOrStdout(), "starting dispatcher (PID %d, workers=%d)\n", os.Getpid(), workers)
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	paths, err := ResolvePaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	cleanStaleWorkerLogs(paths.OroHome, 7*24*time.Hour)

	// Build dispatcher first so we can wire its shutdown authorization flag
	// into the signal handler. This makes the daemon immune to raw SIGTERM
	// until the "shutdown" directive authorizes it.
	d, db, err := buildDispatcher(workers, progressTimeout, reviewTimeout)
	if err != nil {
		return fmt.Errorf("build dispatcher: %w", err)
	}
	defer db.Close()

	wireDependencies(d, paths.SocketPath, paths.OroHome, &dispatcher.ExecCommandRunner{}, true /* daemonOnly */)

	ctx := cmd.Context()
	shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath, d.ShutdownAuthorized())
	defer cleanup()

	if err := d.Run(shutdownCtx); err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "dispatcher stopped")
	return nil
}

// absoluteBeadsDir returns the absolute path to the .beads directory
// relative to the current working directory.
func absoluteBeadsDir() (string, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working dir: %w", err)
	}
	return filepath.Join(repoRoot, ".beads"), nil
}

// bootstrapOroDir creates the oro state directory with 0700 permissions.
// It is idempotent — calling it on an existing directory is a no-op.
func bootstrapOroDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create oro dir %s: %w", dir, err)
	}
	return nil
}

// buildCodeIndex builds the code index in the background.
// It opens the index at dbPath, builds it with the provided context, and closes it.
// Errors (open, build, context cancel) are logged as warnings and never fatal.
// Returns nil in all cases (best-effort background operation).
func buildCodeIndex(ctx context.Context, repoRoot, dbPath string) error {
	// Check for early context cancellation.
	if err := ctx.Err(); err != nil {
		return nil // context already cancelled, return early
	}

	// Open the index with no reranker (building only).
	idx, err := codesearch.NewCodeIndex(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to open code index for building: %v\n", err)
		return nil // open failure is not fatal
	}
	defer idx.Close()

	// Build the index with the provided context.
	_, buildErr := idx.Build(ctx, repoRoot)
	if buildErr != nil {
		fmt.Fprintf(os.Stderr, "warning: code index build failed: %v\n", buildErr)
		return nil // build failure is not fatal
	}

	return nil
}

// buildDispatcher constructs a Dispatcher with all production dependencies.
// The caller owns the returned *sql.DB and must close it.
// Zero-value timeouts use dispatcher defaults (ProgressTimeout=10m, ReviewTimeout=15m).
func buildDispatcher(maxWorkers int, progressTimeout, reviewTimeout time.Duration) (*dispatcher.Dispatcher, *sql.DB, error) {
	// All paths (socket, PID, DB) are now project-scoped via ResolvePaths.
	paths, err := ResolvePaths()
	if err != nil {
		return nil, nil, err
	}
	sockPath := paths.SocketPath
	dbPath := paths.StateDBPath

	// Migrate global DBs to per-project directory on first use.
	// No-op when: no project set, project DB exists, or global DB missing.
	if project := readProjectName(); project != "" {
		if err := migrateGlobalDBs(project); err != nil {
			return nil, nil, fmt.Errorf("migrate global DBs: %w", err)
		}
	}

	db, err := openStateDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}

	// Get repo root for worktree manager.
	repoRoot, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("get working dir: %w", err)
	}

	// Open code index eagerly (fast — just opens SQLite DB) so the
	// dispatcher can serve queries on any previously-built index data.
	// Build runs in the background to refresh the index without blocking startup.
	var codeIdx dispatcher.CodeIndex
	idx, idxErr := codesearch.NewCodeIndex(paths.CodeIndexDBPath)
	if idxErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to open code index: %v\n", idxErr)
	} else {
		idx.SetReranker(codesearch.NewReranker(&codesearch.ClaudeRerankSpawner{}))
		codeIdx = &codeIndexAdapter{idx: idx}
		// Launch best-effort code index build in background (non-blocking).
		go func() {
			_ = buildCodeIndex(context.Background(), repoRoot, paths.CodeIndexDBPath)
		}()
	}

	runner := &dispatcher.ExecCommandRunner{}
	beadSrc := dispatcher.NewCLIBeadSource(runner)
	wtMgr := dispatcher.NewGitWorktreeManager(repoRoot, runner)
	esc := dispatcher.NewTmuxEscalator(TmuxSessionName(readProjectName()), TmuxPaneTarget(readProjectName(), "manager"), runner)

	merger := merge.NewCoordinator(&merge.ExecGitRunner{})
	opsSpawner := ops.NewSpawner(&ops.ClaudeOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath:      sockPath,
		MaxWorkers:      maxWorkers,
		DBPath:          dbPath,
		RepoRoot:        repoRoot,
		ProgressTimeout: progressTimeout,
		ReviewTimeout:   reviewTimeout,
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, codeIdx)
	if err != nil {
		return nil, nil, fmt.Errorf("create dispatcher: %w", err)
	}
	return d, db, nil
}

// wireDependencies attaches production components to the dispatcher.
func wireDependencies(d *dispatcher.Dispatcher, sockPath, oroHome string, runner dispatcher.CommandRunner, daemonOnly bool) {
	d.SetProcessManager(dispatcher.NewOroProcessManager(sockPath, oroHome))
	// Skip pane restarter in daemon-only mode: no tmux session exists, so
	// attempting to restart panes would spam pane_restart_failed events.
	if !daemonOnly {
		// Build the manager pane command using execEnvCmd with the project context
		project := os.Getenv("ORO_PROJECT")
		managerCmd := execEnvCmd("manager", project)
		d.SetPaneRestarter(dispatcher.NewTmuxPaneRestarter(TmuxSessionName(project), managerCmd, runner))
	}
}

// readProjectConfig reads the project name from .oro/config.yaml in the given directory.
// Returns empty string (no error) if the file doesn't exist (backward compat).
func readProjectConfig(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".oro", "config.yaml")) //nolint:gosec // path from trusted dir
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read .oro/config.yaml: %w", err)
	}
	// Simple line-based parsing — avoid YAML dependency for one field.
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "project:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "project:")), nil
		}
	}
	return "", nil
}

// codeIndexAdapter wraps *codesearch.CodeIndex to satisfy dispatcher.CodeIndex.
// It converts codesearch types to dispatcher types.
type codeIndexAdapter struct {
	idx *codesearch.CodeIndex
}

func (a *codeIndexAdapter) FTS5Search(ctx context.Context, query string, limit int) ([]dispatcher.CodeChunk, error) {
	chunks, err := a.idx.FTS5Search(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("code index search: %w", err)
	}
	out := make([]dispatcher.CodeChunk, len(chunks))
	for i, c := range chunks {
		out[i] = dispatcher.CodeChunk{
			FilePath:  c.FilePath,
			Name:      c.Name,
			Kind:      string(c.Kind),
			StartLine: c.StartLine,
			EndLine:   c.EndLine,
			Content:   c.Content,
		}
	}
	return out, nil
}

// Search performs two-phase search (FTS5 + optional Claude reranking).
// On reranker timeout or failure, falls back to FTS5 positional scores. No error is returned.
func (a *codeIndexAdapter) Search(ctx context.Context, query string, topK int) ([]dispatcher.SearchResult, error) {
	results, err := a.idx.Search(ctx, query, topK)
	if err == nil {
		out := make([]dispatcher.SearchResult, len(results))
		for i, r := range results {
			out[i] = dispatcher.SearchResult{
				CodeChunk: dispatcher.CodeChunk{
					FilePath:  r.Chunk.FilePath,
					Name:      r.Chunk.Name,
					Kind:      string(r.Chunk.Kind),
					StartLine: r.Chunk.StartLine,
					EndLine:   r.Chunk.EndLine,
					Content:   r.Chunk.Content,
				},
				Score:  r.Score,
				Reason: r.Reason,
			}
		}
		return out, nil
	}
	// Reranker failed/timed out — fall back to FTS5 positional scores with no Reason.
	// Use a fresh context: the original ctx may already be expired after the reranker timeout.
	freshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chunks, ftsErr := a.idx.FTS5Search(freshCtx, query, topK)
	if ftsErr != nil {
		return nil, nil //nolint:nilerr // best-effort: suppress error, caller gets empty results
	}
	out := make([]dispatcher.SearchResult, len(chunks))
	for i, c := range chunks {
		out[i] = dispatcher.SearchResult{
			CodeChunk: dispatcher.CodeChunk{
				FilePath:  c.FilePath,
				Name:      c.Name,
				Kind:      string(c.Kind),
				StartLine: c.StartLine,
				EndLine:   c.EndLine,
				Content:   c.Content,
			},
			Score: 1.0 / float64(i+1),
		}
	}
	return out, nil
}

// sendStartDirective connects to the dispatcher UDS and sends a "start"
// directive so it transitions from StateInert to StateRunning.
func sendStartDirective(sockPath string) error {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", sockPath)
	if err != nil {
		return fmt.Errorf("connect to dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, "start", ""); err != nil {
		return fmt.Errorf("send start directive: %w", err)
	}
	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	return nil
}
