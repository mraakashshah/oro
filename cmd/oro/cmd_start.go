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

	"oro/pkg/beadstore"
	"oro/pkg/codesearch"
	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/processenv"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// DaemonSpawner abstracts spawning the daemon subprocess for testability.
type DaemonSpawner interface {
	SpawnDaemon(pidPath string, workers, maxWorkers int) (pid int, err error)
}

// ExecDaemonSpawner spawns a real child process running `oro start --daemon-only`.
// Optional timeout fields are forwarded as CLI flags to the child process.
type ExecDaemonSpawner struct {
	ProgressTimeout    time.Duration
	OpsReviewTimeout   time.Duration
	ReviewStallTimeout time.Duration
	ManualIntegration  bool
}

// SetManualIntegration lets dispatcher-only startup configure the daemon
// spawner without widening the core SpawnDaemon interface used by tests.
func (e *ExecDaemonSpawner) SetManualIntegration(enabled bool) {
	e.ManualIntegration = enabled
}

// buildArgs constructs the CLI arguments for the daemon child process.
func (e *ExecDaemonSpawner) buildArgs(workers, maxWorkers int) []string {
	args := []string{"start", "--daemon-only", "--workers", strconv.Itoa(workers), "--max-workers", strconv.Itoa(maxWorkers)}
	if e.ProgressTimeout > 0 {
		args = append(args, "--progress-timeout="+e.ProgressTimeout.String())
	}
	if e.OpsReviewTimeout > 0 {
		args = append(args, "--ops-review-timeout="+e.OpsReviewTimeout.String())
	}
	if e.ReviewStallTimeout > 0 {
		args = append(args, "--review-stall-timeout="+e.ReviewStallTimeout.String())
	}
	if e.ManualIntegration {
		args = append(args, "--manual-integration")
	}
	return args
}

// SpawnDaemon forks a child process running the current binary with --daemon-only.
// The child is placed in its own session (Setsid) so it survives parent exit
// without receiving SIGHUP from the parent's process group.
func (e *ExecDaemonSpawner) SpawnDaemon(pidPath string, workers, maxWorkers int) (int, error) {
	// Use exec.Command (not CommandContext) — the daemon is a long-lived child
	// that must survive parent exit. CommandContext starts an internal goroutine
	// tied to the parent process lifecycle; plain Command avoids this entirely.
	self, err := trustedSelfExecutable()
	if err != nil {
		return 0, err
	}
	child := exec.Command(self, e.buildArgs(workers, maxWorkers)...) //nolint:gosec,noctx // intentionally re-executing self; no context — daemon must outlive parent

	// Redirect daemon stdout/stderr to a log file. Inheriting the parent's
	// stdout/stderr causes SIGPIPE when the parent exits (broken pipe),
	// silently killing the daemon.
	logPath := daemonLogPath(readProjectNameCWD())
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

func trustedSelfExecutable() (string, error) {
	return resolveTrustedSelfExecutable(currentRepoRoot(), os.Args[0], os.Executable, exec.LookPath)
}

func resolveTrustedSelfExecutable(
	repoRoot string,
	argv0 string,
	executable func() (string, error),
	lookPath func(string) (string, error),
) (string, error) {
	self, err := executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	self = cleanExecutablePath(self)

	candidate := argv0
	if !filepath.IsAbs(candidate) {
		resolved, lookErr := lookPath(candidate)
		if lookErr != nil {
			return "", fmt.Errorf("resolve executable %q from PATH: %w", argv0, lookErr)
		}
		candidate = resolved
	}
	candidate = cleanExecutablePath(candidate)

	if isRepoLocalOro(repoRoot, candidate) {
		return "", fmt.Errorf("refusing to re-exec repo-local oro binary %s; current executable is %s", candidate, self)
	}
	return self, nil
}

func cleanExecutablePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if parentErr != nil {
			return abs
		}
		return filepath.Join(parent, filepath.Base(abs))
	}
	return resolved
}

func isRepoLocalOro(repoRoot, candidate string) bool {
	if filepath.Base(candidate) != "oro" {
		return false
	}
	root := cleanExecutablePath(repoRoot)
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func currentRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if fileExists(filepath.Join(cwd, ".git")) || fileExists(filepath.Join(cwd, "go.mod")) {
			return cwd
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return cwd
		}
		cwd = parent
	}
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
	return processenv.ForWorkdir(cleaned, "")
}

// socketPollTimeout is the maximum time to wait for the dispatcher socket.
// 15s allows for DB migrations, code indexing, and schema init on first run.
const socketPollTimeout = 15 * time.Second

// socketPollInterval is how often to check for the socket file.
const socketPollInterval = 50 * time.Millisecond

const daemonSkipPreflightEnv = "ORO_DAEMON_SKIP_PREFLIGHT"

var runDaemonOnlyFn = runDaemonOnly //nolint:gochecknoglobals // test seam for start command flag handoff

func withDaemonPreflightBypass(enabled bool, fn func() error) error {
	if !enabled {
		return fn()
	}
	previous, hadPrevious := os.LookupEnv(daemonSkipPreflightEnv)
	if err := os.Setenv(daemonSkipPreflightEnv, "1"); err != nil {
		return fmt.Errorf("set %s: %w", daemonSkipPreflightEnv, err)
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv(daemonSkipPreflightEnv, previous)
			return
		}
		_ = os.Unsetenv(daemonSkipPreflightEnv)
	}()
	return fn()
}

func shouldSkipDaemonPreflight(daemonOnly bool) bool {
	return daemonOnly &&
		os.Getenv(daemonSkipPreflightEnv) == "1" &&
		nativeProductionBeadSourceMode() == "sqlite"
}

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

func runFullStart(w io.Writer, workers, maxWorkers int, model, project string, spawner DaemonSpawner, tmuxRunner CmdRunner, killFn func(int) error, socketTimeout time.Duration, sleeper func(time.Duration), beaconTimeout time.Duration, detach bool) error {
	// Initialize startup logger (TTY detection for spinner vs static output)
	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	log := newStartupLog(w, isTTY)

	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	pidPath := paths.PIDPath
	sockPath := paths.SocketPath

	log.Step("Preflight checks passed")

	// 1. Spawn the daemon subprocess.
	pid, err := spawner.SpawnDaemon(pidPath, workers, maxWorkers)
	if err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// cleanupOrphans kills the daemon on error after spawn.
	cleanupOrphans := func() {
		if killErr := killFn(pid); killErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to kill orphaned daemon (PID %d): %v\n", pid, killErr)
		}
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
	if err := sess.Create(ManagerNudge()); err != nil {
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
	return preflightAndCheckRunningWith(w, runPreflightChecks, true)
}

// preflightAndCheckRunningWith runs the supplied preflight closure and
// optionally the repo-rooted checks (build oro-search-hook, warn on
// quality_gate.sh / Epic C drift). Hermetic daemon-skip mode passes
// runRepoChecks=false because that mode assumes no Go toolchain and no
// repo on disk (oro-7jjt).
func preflightAndCheckRunningWith(w io.Writer, preflight func() error, runRepoChecks bool) (pidPath string, err error) {
	// Clear CLAUDECODE early — it leaks from Claude Code's Bash tool
	// and blocks nested claude sessions in tmux panes and workers.
	os.Unsetenv("CLAUDECODE")

	if err := preflight(); err != nil {
		return "", fmt.Errorf("preflight checks failed: %w", err)
	}

	// Auto-run oro init if no project config exists (neither standard nor stealth).
	if _, _, detectErr := readProjectName("."); detectErr != nil || !projectInitialized(".") {
		fmt.Fprintf(w, "project not initialized — running oro init (stealth)...\n")
		if initErr := runInit(w, false, false, true, ".", ""); initErr != nil {
			return "", fmt.Errorf("auto-init failed: %w — run 'oro init' manually", initErr)
		}
	}

	paths, err := ResolveDaemonPaths()
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
		regenerateProjectSettings(w, paths.OroHome, readProjectNameCWD())
	}

	if runRepoChecks {
		if err := runRepoPreflightChecks(w, paths.OroHome); err != nil {
			return "", err
		}
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

// runRepoPreflightChecks performs repo-rooted preflight steps:
// builds oro-search-hook (hard-fails if no recovery path) and emits warnings
// about quality_gate.sh + Epic C config drift. No-op when cwd is unreadable.
func runRepoPreflightChecks(w io.Writer, oroHome string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr // cwd unreadable is non-fatal: warnings + hook build are best-effort here
	}

	// Build oro-search-hook if missing or stale. Hard-fails when no recovery
	// path exists (binary missing AND srcDir missing or build broken):
	// settings.json references this binary, so silently degrading turns every
	// PreToolUse Read hook into a missing-binary error.
	//
	// Resolve srcDir from the actual repo root (walk up to find go.mod) rather
	// than cwd — `go test ./cmd/oro/...` runs with cwd inside the package
	// directory, so a naive Join(cwd, "cmd/oro-search-hook") would produce a
	// nonsensical doubled path and trigger false hard-fail (oro-5879).
	srcRoot := walkUpForGoMod(cwd)
	searchHookBin := filepath.Join(oroHome, "hooks", "oro-search-hook")
	if hookErr := ensureSearchHook(os.Stderr, searchHookBin, filepath.Join(srcRoot, "cmd", "oro-search-hook")); hookErr != nil {
		return fmt.Errorf("preflight: %w", hookErr)
	}

	warnIfQualityGateMissing(w, cwd)
	warnIfQualityGateUntracked(w, cwd)
	warnIfEpicCNotDeployed(w, cwd)
	return nil
}

// walkUpForGoMod walks up from start looking for a directory containing go.mod.
// Falls back to start when no go.mod is found anywhere on the way to /.
func walkUpForGoMod(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

func startPreflightAndCheckRunning(w io.Writer, daemonOnly bool) (pidPath string, err error) {
	if shouldSkipDaemonPreflight(daemonOnly) {
		return preflightAndCheckRunningWith(w, runSQLiteDaemonPreflightChecks, false)
	}
	return preflightAndCheckRunning(w)
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

	if err := sess.Create(ManagerNudge()); err != nil {
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
		workers            int
		maxWorkers         int
		daemonOnly         bool
		detach             bool
		model              string
		progressTimeout    time.Duration
		opsReviewTimeout   time.Duration
		reviewStallTimeout time.Duration
		manualIntegration  bool
		baseBranch         string
		webEnabled         bool
		webAddr            string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// When --max-workers is unset (0), default to --workers so the
			// ceiling equals the initial target (backward-compatible behaviour).
			if maxWorkers == 0 {
				maxWorkers = workers
			}
			pidPath, err := startPreflightAndCheckRunning(cmd.OutOrStdout(), daemonOnly)
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
				return runDaemonOnlyFn(cmd, pidPath, workers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, baseBranch, webEnabled, webAddr)
			}
			return startFreshSwarm(cmd.OutOrStdout(), workers, maxWorkers, model, detach, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration)
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn initially")
	cmd.Flags().IntVar(&maxWorkers, "max-workers", 0, "maximum worker ceiling for auto-scale (default: same as --workers)")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")
	cmd.Flags().BoolVarP(&detach, "detach", "D", false, "start in detached mode (don't attach to tmux session)")
	cmd.Flags().DurationVar(&progressTimeout, "progress-timeout", 0, "max time without worker progress before STUCK_WORKER (default 10m)")
	cmd.Flags().DurationVar(&opsReviewTimeout, "ops-review-timeout", 0, "max time for ops review subprocess (default 35m)")
	cmd.Flags().DurationVar(&reviewStallTimeout, "review-stall-timeout", 0, "max time a reviewing worker can stall before STUCK_WORKER (default 15m)")
	cmd.Flags().DurationVar(&reviewStallTimeout, "review-timeout", 0, "deprecated alias for --review-stall-timeout")
	_ = cmd.Flags().MarkHidden("review-timeout")
	cmd.Flags().BoolVar(&manualIntegration, "manual-integration", false, "leave completed worker branches for manual review instead of auto-merging")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", "base branch for worktree creation (default: main)")
	cmd.Flags().BoolVar(&webEnabled, "web", false, "enable HTTP server for dashboard/health endpoints")
	cmd.Flags().StringVar(&webAddr, "web-addr", "", "HTTP server listen address (default 127.0.0.1:4444)")

	return cmd
}

// startFreshSwarm sets up project env vars and launches the full swarm (daemon + tmux).
func startFreshSwarm(w io.Writer, workers, maxWorkers int, model string, detach bool, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool) error {
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
	if err := requireNativeProductionBeadSourceMode("oro start"); err != nil {
		return err
	}
	return runFullStart(w, workers, maxWorkers, model, project,
		&ExecDaemonSpawner{ProgressTimeout: progressTimeout, OpsReviewTimeout: opsReviewTimeout, ReviewStallTimeout: reviewStallTimeout, ManualIntegration: manualIntegration},
		&ExecRunner{},
		func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) },
		socketPollTimeout, nil, 0, isDetached(detach),
	)
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
func runDaemonOnly(cmd *cobra.Command, pidPath string, workers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, webEnabled bool, webAddr string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "starting dispatcher (PID %d, workers=%d)\n", os.Getpid(), workers)
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	cleanStaleWorkerLogs(paths.OroHome, 7*24*time.Hour)

	// Build dispatcher first so we can wire its shutdown authorization flag
	// into the signal handler. This makes the daemon immune to raw SIGTERM
	// until the "shutdown" directive authorizes it.
	d, db, err := buildDispatcherWithReviewTimeouts(workers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, baseBranch, webEnabled, webAddr)
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

// absoluteBeadsDir returns the absolute path to the beads directory
// for the current project (respects stealth mode).
func absoluteBeadsDir() (string, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working dir: %w", err)
	}
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve paths: %w", err)
	}
	return paths.BeadsDir, nil
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
// Zero-value timeouts use dispatcher/ops defaults.
// The initial target and auto-scale ceiling both start at one worker for
// callers that do not need timeout controls.
func buildDispatcher(baseBranch string, webEnabled bool, webAddr string) (*dispatcher.Dispatcher, *sql.DB, error) {
	return buildDispatcherWithReviewTimeouts(1, 1, 0, 0, 0, false, baseBranch, webEnabled, webAddr)
}

// buildDispatcherWithReviewTimeouts constructs a Dispatcher with separate
// ops-review subprocess and reviewing-worker stall timeout controls.
func buildDispatcherWithReviewTimeouts(initialWorkers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, webEnabled bool, webAddr string) (*dispatcher.Dispatcher, *sql.DB, error) { //nolint:funlen // factory initialization
	if err := requireNativeProductionBeadSourceMode("oro start"); err != nil {
		return nil, nil, err
	}
	runtime, err := resolveProductionRuntime()
	if err != nil {
		return nil, nil, err
	}
	// All paths (socket, PID, DB) are now project-scoped via ResolvePaths.
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return nil, nil, err
	}
	sockPath := paths.SocketPath
	dbPath := paths.StateDBPath
	// Migrate global DBs to per-project directory on first use (no-op if already migrated).
	if project := readProjectNameCWD(); project != "" {
		if err := migrateGlobalDBs(project); err != nil {
			return nil, nil, fmt.Errorf("migrate global DBs: %w", err)
		}
	}

	db, err := openStateDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("get working dir: %w", err)
	}

	// Open code index eagerly (fast — just opens SQLite DB) so the dispatcher
	// can serve queries on previously-built data. Background build refreshes it.
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
	projectPaths, _ := ResolvePaths(repoRoot)
	runner := &dispatcher.ExecCommandRunner{}
	beadSrc := beadstore.NewSQLiteStore(db)
	wtMgr := dispatcher.NewGitWorktreeManager(repoRoot, "", projectPaths.QualityGate, runner)
	esc := dispatcher.NewTmuxEscalator(TmuxSessionName(readProjectNameCWD()), TmuxPaneTarget(readProjectNameCWD(), "manager"), runner)
	merger := merge.NewCoordinator(&merge.ExecGitRunner{})
	opsSpawner := ops.NewSpawnerWithReviewTimeout(runtime.opsSpawn, opsReviewTimeout)

	cfg := dispatcher.Config{
		SocketPath:              sockPath,
		InitialWorkers:          initialWorkers,
		MaxWorkers:              maxWorkers,
		AllowZeroWorkers:        initialWorkers == 0,
		DBPath:                  dbPath,
		RepoRoot:                repoRoot,
		ProgressTimeout:         progressTimeout,
		ReviewTimeout:           reviewStallTimeout,
		ManualIntegration:       manualIntegration,
		WorkerProgram:           resolveWorkerProgramPath(repoRoot),
		ReviewPatternCandidates: resolveReviewPatternCandidatesPath(repoRoot),
		DefaultBranch:           baseBranch,
		DreamInterval:           10,
		WebEnabled:              webEnabled,
		WebAddr:                 webAddr,
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, codeIdx)
	if err != nil {
		return nil, nil, fmt.Errorf("create dispatcher: %w", err)
	}
	return d, db, nil
}

// resolveWorkerProgramPath returns the worker-program.md path for repoRoot.
// Falls back to <repoRoot>/worker-program.md if path resolution fails.
func resolveWorkerProgramPath(repoRoot string) string {
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		return filepath.Join(repoRoot, "worker-program.md")
	}
	return paths.WorkerProgram
}

// resolveReviewPatternCandidatesPath returns the review-pattern-candidates path
// for repoRoot. Falls back to <repoRoot>/.oro/review-pattern-candidates.md if
// path resolution fails.
func resolveReviewPatternCandidatesPath(repoRoot string) string {
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		return filepath.Join(repoRoot, ".oro", "review-pattern-candidates.md")
	}
	return paths.ReviewPatternCandidates
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
	return a.SearchInWorkdir(ctx, query, topK, "")
}

func (a *codeIndexAdapter) SearchInWorkdir(ctx context.Context, query string, topK int, workdir string) ([]dispatcher.SearchResult, error) {
	results, err := a.idx.SearchInWorkdir(ctx, query, topK, workdir)
	if err == nil {
		return adaptCodeSearchResults(results), nil
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

func adaptCodeSearchResults(results []codesearch.SearchResult) []dispatcher.SearchResult {
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
	return out
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

	// Set a 10-second read deadline for receiving the ACK response
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}

	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	return nil
}
