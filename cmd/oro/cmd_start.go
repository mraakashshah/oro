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
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// DaemonSpawner abstracts spawning the daemon subprocess for testability.
type DaemonSpawner interface {
	SpawnDaemon(pidPath string, workers int) (pid int, err error)
}

// ExecDaemonSpawner spawns a real child process running `oro start --daemon-only`.
type ExecDaemonSpawner struct{}

// SpawnDaemon forks a child process running the current binary with --daemon-only.
// The child is placed in its own session (Setsid) so it survives parent exit
// without receiving SIGHUP from the parent's process group.
func (e *ExecDaemonSpawner) SpawnDaemon(pidPath string, workers int) (int, error) {
	// Use exec.Command (not CommandContext) — the daemon is a long-lived child
	// that must survive parent exit. CommandContext starts an internal goroutine
	// tied to the parent process lifecycle; plain Command avoids this entirely.
	child := exec.Command(os.Args[0], "start", "--daemon-only", "--workers", strconv.Itoa(workers)) //nolint:gosec,noctx // intentionally re-executing self; no context — daemon must outlive parent

	// Redirect daemon stdout/stderr to a log file. Inheriting the parent's
	// stdout/stderr causes SIGPIPE when the parent exits (broken pipe),
	// silently killing the daemon.
	logPath := filepath.Join(os.TempDir(), "oro-daemon.log")
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
const socketPollTimeout = 5 * time.Second

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
func runFullStart(w io.Writer, workers int, model, project string, spawner DaemonSpawner, tmuxRunner CmdRunner, socketTimeout time.Duration, sleeper func(time.Duration), beaconTimeout time.Duration, detach bool) error {
	pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
	if err != nil {
		return fmt.Errorf("get pid path: %w", err)
	}
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return fmt.Errorf("get socket path: %w", err)
	}

	// 1. Spawn the daemon subprocess.
	pid, err := spawner.SpawnDaemon(pidPath, workers)
	if err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// 2. Wait for the dispatcher socket to appear.
	deadline := time.Now().Add(socketTimeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		time.Sleep(socketPollInterval)
	}
	if _, err := os.Stat(sockPath); err != nil {
		return fmt.Errorf("dispatcher socket not ready at %s: %w", sockPath, err)
	}

	// 2b. Send start directive so dispatcher transitions from Inert to Running.
	if err := sendStartDirective(sockPath); err != nil {
		return fmt.Errorf("send start directive: %w", err)
	}

	// 3. Create tmux session with short nudges (full role context injected by SessionStart hook).
	sess := &TmuxSession{Name: "oro", Project: project, Runner: tmuxRunner, Sleeper: sleeper, BeaconTimeout: beaconTimeout}
	if err := sess.Create(ArchitectNudge(), ManagerNudge()); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	// 4. Print status.
	fmt.Fprintf(w, "oro swarm started (PID %d, workers=%d, model=%s)\n", pid, workers, model)

	// 5. Attach interactively, or print instructions if detached.
	if detach {
		fmt.Fprintln(w, "detached — attach with: tmux attach -t oro")
		return nil
	}
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

	oroDir, err := defaultOroDir()
	if err != nil {
		return "", fmt.Errorf("get oro dir: %w", err)
	}
	if err := bootstrapOroDir(oroDir); err != nil {
		return "", fmt.Errorf("bootstrap oro dir: %w", err)
	}

	pidPath, err = oroPath("ORO_PID_PATH", "oro.pid")
	if err != nil {
		return "", fmt.Errorf("get pid path: %w", err)
	}
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return "", fmt.Errorf("get socket path: %w", err)
	}

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
	case StatusStopped:
		// Good to go.
	}

	return pidPath, nil
}

// newStartCmd creates the "oro start" subcommand.
func newStartCmd() *cobra.Command {
	var (
		workers    int
		daemonOnly bool
		detach     bool
		model      string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		Long:  "Creates a tmux session with the full Oro layout and begins autonomous execution.\nStarts the dispatcher daemon and worker pool in the background.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := preflightAndCheckRunning(cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if pidPath == "" {
				return nil // already running
			}

			if daemonOnly {
				return runDaemonOnly(cmd, pidPath, workers)
			}

			// Read project identity from .oro/config.yaml (if present).
			project, err := readProjectConfig(".")
			if err != nil {
				return fmt.Errorf("read project config: %w", err)
			}

			// Set ORO_PROJECT and ORO_HOME for child processes (daemon inherits env).
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

			return runFullStart(cmd.OutOrStdout(), workers, model, project, &ExecDaemonSpawner{}, &ExecRunner{}, socketPollTimeout, nil, 0, isDetached(detach))
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")
	cmd.Flags().BoolVarP(&detach, "detach", "D", false, "start in detached mode (don't attach to tmux session)")

	return cmd
}

// runDaemonOnly runs the dispatcher in the foreground (used for testing/CI).
func runDaemonOnly(cmd *cobra.Command, pidPath string, workers int) error {
	fmt.Fprintf(cmd.OutOrStdout(), "starting dispatcher (PID %d, workers=%d)\n", os.Getpid(), workers)
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	// Build dispatcher first so we can wire its shutdown authorization flag
	// into the signal handler. This makes the daemon immune to raw SIGTERM
	// until the "shutdown" directive authorizes it.
	d, db, err := buildDispatcher(workers)
	if err != nil {
		return fmt.Errorf("build dispatcher: %w", err)
	}
	defer db.Close()

	ctx := cmd.Context()
	shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath, d.ShutdownAuthorized())
	defer cleanup()

	if err := d.Run(shutdownCtx); err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "dispatcher stopped")
	return nil
}

// defaultOroDir returns the default ~/.oro directory path.
func defaultOroDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, protocol.OroDir), nil
}

// bootstrapOroDir creates the oro state directory with 0700 permissions.
// It is idempotent — calling it on an existing directory is a no-op.
func bootstrapOroDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create oro dir %s: %w", dir, err)
	}
	return nil
}

// oroDir returns the resolved path, respecting ORO_*_PATH env overrides.
func oroPath(envKey, defaultSuffix string) (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, protocol.OroDir, defaultSuffix), nil
}

// buildDispatcher constructs a Dispatcher with all production dependencies.
// The caller owns the returned *sql.DB and must close it.
func buildDispatcher(maxWorkers int) (*dispatcher.Dispatcher, *sql.DB, error) {
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return nil, nil, err
	}
	dbPath, err := oroPath("ORO_DB_PATH", "state.db")
	if err != nil {
		return nil, nil, err
	}

	db, err := openDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}

	// Apply schema migrations for older databases (columns may already exist).
	migrateStateDB(db)

	// Best-effort knowledge ingest on startup (missing file is not an error).
	ingestKnowledgeOnStartup(db)

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
	idx, idxErr := codesearch.NewCodeIndex(defaultIndexDBPath(), nil)
	if idxErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to open code index: %v\n", idxErr)
	} else {
		codeIdx = &codeIndexAdapter{idx: idx}
		// Launch best-effort code index build in background (non-blocking).
		go func() {
			_, buildErr := idx.Build(context.Background(), repoRoot)
			if buildErr != nil {
				fmt.Fprintf(os.Stderr, "warning: code index build failed: %v\n", buildErr)
			}
		}()
	}

	runner := &dispatcher.ExecCommandRunner{}
	beadSrc := dispatcher.NewCLIBeadSource(runner)
	wtMgr := dispatcher.NewGitWorktreeManager(repoRoot, runner)
	esc := dispatcher.NewTmuxEscalator("", "", runner) // defaults: session "oro", pane "oro:manager"

	merger := merge.NewCoordinator(&merge.ExecGitRunner{})
	opsSpawner := ops.NewSpawner(&ops.ClaudeOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath: sockPath,
		MaxWorkers: maxWorkers,
		DBPath:     dbPath,
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, codeIdx)
	if err != nil {
		return nil, nil, fmt.Errorf("create dispatcher: %w", err)
	}
	d.SetProcessManager(dispatcher.NewOroProcessManager(sockPath))
	return d, db, nil
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
// It converts []codesearch.Chunk to []dispatcher.CodeChunk.
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

// ingestKnowledgeOnStartup attempts to ingest knowledge.jsonl on dispatcher startup.
// Best-effort: missing file or errors are silently ignored (logged to stderr).
func ingestKnowledgeOnStartup(db *sql.DB) {
	// Resolve knowledge file path (same priority as oro ingest command)
	knowledgePath, err := resolveKnowledgeFile("")
	if err != nil {
		// No knowledge file found - skip silently
		return
	}

	// Open file
	f, err := os.Open(knowledgePath) //nolint:gosec // path from trusted sources
	if err != nil {
		// Can't open file - skip silently
		return
	}
	defer f.Close()

	// Create memory store and ingest
	store := memory.NewStore(db)
	ctx := context.Background()
	count, err := memory.IngestKnowledge(ctx, store, f)
	if err != nil {
		// Ingest error - log but don't block startup
		fmt.Fprintf(os.Stderr, "warning: knowledge ingest failed: %v\n", err)
		return
	}

	if count > 0 {
		fmt.Fprintf(os.Stderr, "info: ingested %d knowledge entries from %s\n", count, knowledgePath)
	}
}
