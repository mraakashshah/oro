package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oro/pkg/agentassets"
	"oro/pkg/agentmodel"
	"oro/pkg/beadstore"
	"oro/pkg/codesearch"
	"oro/pkg/dispatcher"
	"oro/pkg/factoryhealth"
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
// Optional fields are forwarded as CLI flags to the child process.
type ExecDaemonSpawner struct {
	ProgressTimeout    time.Duration
	OpsReviewTimeout   time.Duration
	ReviewStallTimeout time.Duration
	BaseRef            string
	TargetBranch       string
	// BaseBranch is the legacy single-branch handoff. New callers should set
	// BaseRef and TargetBranch separately.
	BaseBranch        string
	ManualIntegration bool
	MutationTesting   bool
	WebEnabled        bool
	WebAddr           string
	Cleanliness       cleanlinessStartConfig
}

type cleanlinessStartConfig struct {
	JanitorInterval      int
	JanitorIdleThreshold int
	AuditEveryNJanitors  int
	JanitorTopK          int
	JanitorEnabled       bool
	AuditEnabled         bool
}

type startBranchConfig struct {
	BaseRef      string
	TargetBranch string
}

func resolveStartBranchConfig(ctx context.Context, baseRef, targetBranch, legacyBaseBranch string) (startBranchConfig, error) {
	branches := startBranchConfig{
		BaseRef:      strings.TrimSpace(baseRef),
		TargetBranch: strings.TrimSpace(targetBranch),
	}
	legacyBaseBranch = strings.TrimSpace(legacyBaseBranch)
	if branches.TargetBranch == "" {
		branches.TargetBranch = legacyBaseBranch
	}
	if branches.TargetBranch == "" {
		branches.TargetBranch = "main"
	}
	if branches.BaseRef == "" {
		branches.BaseRef = legacyBaseBranch
	}
	if branches.BaseRef == "" {
		branches.BaseRef = branches.TargetBranch
	}
	if startTargetIsRemoteTrackingRef(ctx, branches.TargetBranch) {
		return startBranchConfig{}, fmt.Errorf("target branch %q is remote-tracking; choose an explicit writable local branch", branches.TargetBranch)
	}
	return branches, nil
}

func startTargetIsRemoteTrackingRef(ctx context.Context, targetBranch string) bool {
	targetBranch = strings.TrimSpace(targetBranch)
	if strings.HasPrefix(targetBranch, "refs/remotes/") {
		return true
	}
	remote, _, ok := strings.Cut(targetBranch, "/")
	if !ok || remote == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return remote == "origin"
	}
	for _, name := range strings.Fields(string(output)) {
		if remote == name {
			return true
		}
	}
	return false
}

func defaultCleanlinessStartConfig() cleanlinessStartConfig {
	return cleanlinessStartConfig{
		JanitorInterval:      50,
		JanitorIdleThreshold: 0,
		AuditEveryNJanitors:  5,
		JanitorTopK:          5,
		JanitorEnabled:       true,
		AuditEnabled:         true,
	}
}

// SetManualIntegration lets dispatcher-only startup configure the daemon
// spawner without widening the core SpawnDaemon interface used by tests.
func (e *ExecDaemonSpawner) SetManualIntegration(enabled bool) {
	e.ManualIntegration = enabled
}

// SetMutationTesting lets dispatcher-only startup configure daemon mutation opt-in.
func (e *ExecDaemonSpawner) SetMutationTesting(enabled bool) {
	e.MutationTesting = enabled
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
	baseRef := strings.TrimSpace(e.BaseRef)
	targetBranch := strings.TrimSpace(e.TargetBranch)
	if baseRef != "" || targetBranch != "" {
		if baseRef != "" {
			args = append(args, "--base-ref="+baseRef)
		}
		if targetBranch != "" {
			args = append(args, "--target-branch="+targetBranch)
		}
	} else if baseBranch := strings.TrimSpace(e.BaseBranch); baseBranch != "" {
		args = append(args, "--base-branch="+baseBranch)
	}
	if e.ManualIntegration {
		args = append(args, "--manual-integration")
	}
	if e.MutationTesting {
		args = append(args, "--mutation-testing")
	}
	if e.WebEnabled {
		args = append(args, "--web")
	} else {
		args = append(args, "--web=false")
	}
	if e.WebAddr != "" {
		args = append(args, "--web-addr="+e.WebAddr)
	}
	args = append(args,
		"--janitor-interval="+strconv.Itoa(e.Cleanliness.JanitorInterval),
		"--janitor-idle-threshold="+strconv.Itoa(e.Cleanliness.JanitorIdleThreshold),
		"--audit-every-n-janitors="+strconv.Itoa(e.Cleanliness.AuditEveryNJanitors),
		"--janitor-top-k="+strconv.Itoa(e.Cleanliness.JanitorTopK),
		"--janitor-enabled="+strconv.FormatBool(e.Cleanliness.JanitorEnabled),
		"--audit-enabled="+strconv.FormatBool(e.Cleanliness.AuditEnabled),
	)
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

	child.Env = daemonChildEnv(os.Environ())
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
	start := cwd
	for {
		if pathExists(filepath.Join(cwd, ".git")) || fileExists(filepath.Join(cwd, "go.mod")) {
			return cwd
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return start
		}
		cwd = parent
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

func daemonChildEnv(env []string) []string {
	return withEnvValue(cleanEnvForDaemon(env), tmuxManagedDaemonEnv, "1")
}

func withEnvValue(env []string, name, value string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}

// socketPollTimeout covers the boot-path cache sweep plus a bounded margin for
// DB migrations, code indexing, and schema initialization before the socket opens.
const (
	startupReadinessMargin = 5 * time.Second
	socketPollTimeout      = startupDevCacheSweepBudget + startupReadinessMargin
)

// socketPollInterval is how often to check for the socket file.
const socketPollInterval = 50 * time.Millisecond

const (
	daemonSkipPreflightEnv = "ORO_DAEMON_SKIP_PREFLIGHT"
	tmuxManagedDaemonEnv   = "ORO_TMUX_MANAGED_DAEMON"
)

var (
	runDaemonOnlyFn = runDaemonOnly //nolint:gochecknoglobals // test seam for start command flag handoff
	runFullStartFn  = runFullStart  //nolint:gochecknoglobals // test seam for detached-start flag handoff
)

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

	// 3. Create tmux attach surface. Workers are managed by the daemon; no
	// manager runtime is launched by default.
	sess := &TmuxSession{Name: TmuxSessionName(project), Project: project, Runner: tmuxRunner, Sleeper: sleeper, BeaconTimeout: beaconTimeout}
	if err := sess.Create(); err != nil {
		cleanupOrphans()
		return fmt.Errorf("create tmux session: %w", err)
	}

	log.Step("Tmux session created")
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

	if alreadyRunning := reportAlreadyRunningBeforePreflight(w); alreadyRunning {
		return "", nil
	}

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

	if err := maybeRunRepoPreflightChecks(w, paths.OroHome, runRepoChecks); err != nil {
		return "", err
	}
	if err := ensureRuntimeProjectAssetsWithSearchHook(w, paths.OroHome, runRepoChecks); err != nil {
		return "", err
	}

	pidPath = paths.PIDPath
	sockPath := paths.SocketPath

	if alreadyRunning, err := handleDaemonStatusForStart(w, pidPath, sockPath); err != nil {
		return "", err
	} else if alreadyRunning {
		return "", nil
	}

	return pidPath, nil
}

func handleDaemonStatusForStart(w io.Writer, pidPath, sockPath string) (bool, error) {
	status, pid, err := DaemonStatus(pidPath, sockPath)
	if err != nil {
		return false, fmt.Errorf("get daemon status: %w", err)
	}

	switch status {
	case StatusRunning:
		fmt.Fprintf(w, "dispatcher already running (PID %d)\n", pid)
		return true, nil
	case StatusStale:
		_ = RemovePIDFile(pidPath)
		_ = os.Remove(sockPath)
	case StatusStopped:
		// Good to go.
	}

	return false, nil
}

// reportAlreadyRunningBeforePreflight reports an existing live daemon before
// startup preflight mutates assets or builds helper binaries. Starting an
// already running factory should be an observation, not a repo-preflight
// operation.
func reportAlreadyRunningBeforePreflight(w io.Writer) bool {
	paths, pathErr := ResolveDaemonPaths()
	if pathErr != nil {
		return false
	}
	status, pid, statusErr := DaemonStatus(paths.PIDPath, paths.SocketPath)
	if statusErr != nil {
		return false
	}
	switch status {
	case StatusRunning:
		fmt.Fprintf(w, "dispatcher already running (PID %d)\n", pid)
		return true
	case StatusStale:
		_ = RemovePIDFile(paths.PIDPath)
		_ = os.Remove(paths.SocketPath)
	case StatusStopped:
	}
	return false
}

func maybeRunRepoPreflightChecks(w io.Writer, oroHome string, runRepoChecks bool) error {
	return maybeRunRepoPreflightChecksWith(w, oroHome, runRepoChecks, runRepoPreflightChecks)
}

func maybeRunRepoPreflightChecksWith(w io.Writer, oroHome string, runRepoChecks bool, check func(io.Writer, string) error) error {
	if !runRepoChecks {
		return nil
	}
	return check(w, oroHome)
}

func ensureRuntimeProjectAssets(w io.Writer, oroHome string) error {
	return ensureRuntimeProjectAssetsWithSearchHook(w, oroHome, true)
}

func ensureRuntimeProjectAssetsWithSearchHook(w io.Writer, oroHome string, installSearchHook bool) error {
	assets, err := fs.Sub(EmbeddedAssets, "_assets")
	if err != nil {
		return fmt.Errorf("access embedded assets: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir for runtime assets: %w", err)
	}
	projectRoot := walkUpForGoMod(cwd)
	if err := extractAgentsMDW(projectRoot, assets, false, w); err != nil {
		return fmt.Errorf("extract AGENTS.md: %w", err)
	}

	if !codexAssetsRequired() {
		return nil
	}

	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve user home for Codex assets: %w", err)
		}
		codexHome = filepath.Join(homeDir, ".codex")
	}
	if err := copySkills(agentAssetsConfig{
		oroSkillsDir:       filepath.Join(oroHome, ".claude", "skills"),
		destSkillsDir:      filepath.Join(codexHome, "skills"),
		requireUsingSkills: true,
	}, w); err != nil {
		return fmt.Errorf("install Codex skills: %w", err)
	}
	if err := agentassets.InstallCodexRules(context.Background(), codexHome, agentassets.CodexRuleAssets()); err != nil {
		return fmt.Errorf("install Codex rules: %w", err)
	}
	if installSearchHook {
		if err := ensureSearchHook(os.Stderr, filepath.Join(oroHome, "hooks", "oro-search-hook"), filepath.Join(projectRoot, "cmd", "oro-search-hook")); err != nil {
			return fmt.Errorf("install Codex search hook: %w", err)
		}
	}
	if err := installCodexHookConfig(codexHome, filepath.Join(oroHome, "hooks")); err != nil {
		return fmt.Errorf("install Codex hook config: %w", err)
	}
	return nil
}

func codexAssetsRequired() bool {
	return readAgentRuntime() == runtimeCodex || agentmodel.UsesRuntime(runtimeCodex)
}

const (
	codexOroHooksBegin = "# BEGIN managed by oro: hooks"
	codexOroHooksEnd   = "# END managed by oro: hooks"
)

func installCodexHookConfig(codexHome, hooksDir string) error {
	cleanCodexHome, err := filepath.Abs(codexHome)
	if err != nil {
		return fmt.Errorf("resolve Codex config dir: %w", err)
	}
	codexHome = cleanCodexHome
	// Defense-in-depth: refuse to write a managed block whose hook commands point
	// at an ephemeral hooksDir (under the temp root) into a codexHome that lives
	// outside it. That mismatch means a test isolated ORO_HOME but forgot
	// CODEX_HOME and is about to leak dangling hook paths into the developer's
	// real ~/.codex/config.toml — fail loudly instead of corrupting it.
	if hookPathsWouldLeak(codexHome, hooksDir) {
		return fmt.Errorf(
			"refusing to install Codex hook config: hooks dir %q is under the temp root %q but Codex home %q is not — "+
				"writing would leak ephemeral hook paths into a persistent config (isolate the test with CODEX_HOME)",
			hooksDir, os.TempDir(), codexHome)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(configPath) //nolint:gosec // CODEX_HOME-controlled config path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read Codex config: %w", err)
	}
	next := replaceManagedCodexHookBlock(string(data), codexHookConfigBlock(hooksDir))
	if err := os.MkdirAll(codexHome, 0o700); err != nil { //nolint:gosec // CODEX_HOME is intentionally user-controlled.
		return fmt.Errorf("create Codex config dir: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(next), 0o600); err != nil { //nolint:gosec // config can contain user-local settings
		return fmt.Errorf("write Codex config: %w", err)
	}
	return nil
}

// hookPathsWouldLeak reports whether installing a managed hooks block that points
// at hooksDir into codexHome would leak ephemeral paths into a persistent config.
// It checks common temporary roots rather than just os.TempDir because Oro
// subprocesses may use /tmp while macOS reports a per-user /var/folders temp dir.
func hookPathsWouldLeak(codexHome, hooksDir string) bool {
	var codexUnderTemp, hooksUnderTemp bool
	tempRoots := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders"}
	if goTempDir := os.Getenv("GOTMPDIR"); goTempDir != "" {
		tempRoots = append(tempRoots, goTempDir)
	}
	for _, tempRoot := range tempRoots {
		codexUnderTemp = codexUnderTemp || pathUnder(tempRoot, codexHome)
		hooksUnderTemp = hooksUnderTemp || pathUnder(tempRoot, hooksDir)
	}
	return hooksUnderTemp && !codexUnderTemp
}

// pathUnder reports whether target is root itself or nested inside root, comparing
// absolute paths resolved through existing symlinked ancestors so aliases such as
// /tmp and /private/tmp compare consistently.
func pathUnder(root, target string) bool {
	rootAbs, err := resolvePath(root)
	if err != nil {
		return false
	}
	targetAbs, err := resolvePath(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath returns an absolute path with symlinked ancestors resolved. It can
// canonicalize paths that do not exist yet, which matters for hooksDir paths whose
// parent sandbox is created after the leak guard runs.
func resolvePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	for ancestor := absPath; ; ancestor = filepath.Dir(ancestor) {
		resolved, evalErr := filepath.EvalSymlinks(ancestor)
		if evalErr == nil {
			rel, relErr := filepath.Rel(ancestor, absPath)
			if relErr != nil {
				return "", fmt.Errorf("resolve path relative to existing ancestor: %w", relErr)
			}
			return filepath.Clean(filepath.Join(resolved, rel)), nil
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return absPath, nil
		}
	}
}

func replaceManagedCodexHookBlock(existing, block string) string {
	existing = strings.TrimRight(existing, "\n")
	begin := strings.Index(existing, codexOroHooksBegin)
	end := strings.Index(existing, codexOroHooksEnd)
	if begin >= 0 && end >= begin {
		end += len(codexOroHooksEnd)
		prefix := strings.TrimRight(existing[:begin], "\n")
		suffix := strings.TrimLeft(existing[end:], "\n")
		parts := []string{}
		if prefix != "" {
			parts = append(parts, prefix)
		}
		parts = append(parts, block)
		if suffix != "" {
			parts = append(parts, suffix)
		}
		return strings.Join(parts, "\n\n") + "\n"
	}
	if existing == "" {
		return block + "\n"
	}
	return existing + "\n\n" + block + "\n"
}

func codexHookConfigBlock(hooksDir string) string {
	q := strconv.Quote
	py := func(name string) string { return q("python3 " + filepath.Join(hooksDir, name)) }
	sh := func(name string) string { return q(filepath.Join(hooksDir, name)) }

	return strings.Join([]string{
		codexOroHooksBegin,
		"[hooks]",
		"SessionStart = [",
		"  { matcher = \"\", hooks = [ { type = \"command\", command = " + py("session_start_global.py") + ", async = false } ] },",
		"]",
		"PreToolUse = [",
		"  { matcher = \"Bash\", hooks = [ { type = \"command\", command = " + py("enforce_skills.py") + ", async = false }, { type = \"command\", command = " + py("destructive_command_guard.py") + ", async = false }, { type = \"command\", command = " + sh("oro-search-hook") + ", async = false, timeoutSec = 5, statusMessage = \"Searching codebase...\" } ] },",
		"  { matcher = \"apply_patch\", hooks = [ { type = \"command\", command = " + py("enforce_worktree_writes.py") + ", async = false } ] },",
		"]",
		"PostToolUse = [",
		"  { matcher = \"Bash\", hooks = [ { type = \"command\", command = " + py("prompt_injection_guard.py") + ", async = false }, { type = \"command\", command = " + py("context_pruner.py") + ", async = false } ] },",
		"  { matcher = \"apply_patch\", hooks = [ { type = \"command\", command = " + sh("auto-format.sh") + ", async = false } ] },",
		"]",
		"Stop = [",
		"  { matcher = \"\", hooks = [ { type = \"command\", command = " + py("context_block_stop.py") + ", async = false }, { type = \"command\", command = " + sh("stop-checklist.sh") + ", async = false } ] },",
		"]",
		codexOroHooksEnd,
	}, "\n")
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

// reconnectRunningDaemon handles the case where a daemon is already running.
// It activates the dispatcher if inert, then reconnects (or returns early in
// daemon-only mode).
func reconnectRunningDaemon(w io.Writer, workers int, daemonOnly, detach bool) error {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return fmt.Errorf("resolve daemon paths: %w", err)
	}
	if err := activateInertDispatcher(paths.SocketPath, workers); err != nil {
		return fmt.Errorf("activate dispatcher: %w", err)
	}
	if daemonOnly {
		return nil
	}
	project, _ := readProjectConfig(".")
	return reconnectTmux(w, &ExecRunner{}, project, isDetached(detach), nil, 0)
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

	if err := sess.Create(); err != nil {
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
		baseRef            string
		targetBranch       string
		mutationTesting    bool
		webEnabled         bool
		noWeb              bool
		webAddr            string
		cleanliness        = defaultCleanlinessStartConfig()
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		RunE: func(cmd *cobra.Command, args []string) error {
			branches, err := resolveStartBranchConfig(cmd.Context(), baseRef, targetBranch, baseBranch)
			if err != nil {
				return err
			}
			if err := verifyStartCommandRemoteCapabilities(cmd.Context()); err != nil {
				return err
			}
			if noWeb {
				webEnabled = false
			}
			// Default an unset --max-workers ceiling to --workers for backward compatibility.
			maxWorkers = resolvedMaxWorkers(workers, maxWorkers)
			pidPath, err := startPreflightAndCheckRunning(cmd.OutOrStdout(), daemonOnly)
			if err != nil {
				return err
			}
			if pidPath == "" {
				return reconnectRunningDaemon(cmd.OutOrStdout(), workers, daemonOnly, detach)
			}
			if daemonOnly {
				return runDaemonOnlyFn(cmd, pidPath, workers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, baseBranch, mutationTesting, webEnabled, webAddr, cleanliness)
			}
			return startFreshSwarmWithBranches(cmd.OutOrStdout(), workers, maxWorkers, model, detach, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, branches, mutationTesting, webEnabled, webAddr, cleanliness)
		},
	}

	registerStartCommandFlags(
		cmd, &workers, &maxWorkers, &daemonOnly, &detach, &model,
		&progressTimeout, &opsReviewTimeout, &reviewStallTimeout,
		&manualIntegration, &baseBranch, &baseRef, &targetBranch,
		&mutationTesting, &webEnabled, &noWeb, &webAddr, &cleanliness,
	)

	return cmd
}

func registerStartCommandFlags(
	cmd *cobra.Command,
	workers, maxWorkers *int,
	daemonOnly, detach *bool,
	model *string,
	progressTimeout, opsReviewTimeout, reviewStallTimeout *time.Duration,
	manualIntegration *bool,
	baseBranch, baseRef, targetBranch *string,
	mutationTesting, webEnabled, noWeb *bool,
	webAddr *string,
	cleanliness *cleanlinessStartConfig,
) {
	cmd.Flags().IntVarP(workers, "workers", "w", 2, "number of workers to spawn initially")
	cmd.Flags().IntVar(maxWorkers, "max-workers", 0, "maximum worker ceiling for auto-scale (default: same as --workers)")
	cmd.Flags().BoolVarP(daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(model, "model", "balanced", "routing tier (fast/balanced/deep/background) or provider-native model for manager session")
	cmd.Flags().BoolVarP(detach, "detach", "D", false, "start in detached mode (don't attach to tmux session)")
	cmd.Flags().DurationVar(progressTimeout, "progress-timeout", 0, "max time without worker progress before STUCK_WORKER (default 10m)")
	cmd.Flags().DurationVar(opsReviewTimeout, "ops-review-timeout", 0, "max time for ops review subprocess (default 35m)")
	cmd.Flags().DurationVar(reviewStallTimeout, "review-stall-timeout", 0, "max time a reviewing worker can stall before STUCK_WORKER (default 15m)")
	cmd.Flags().DurationVar(reviewStallTimeout, "review-timeout", 0, "deprecated alias for --review-stall-timeout")
	_ = cmd.Flags().MarkHidden("review-timeout")
	cmd.Flags().BoolVar(manualIntegration, "manual-integration", false, "leave completed worker branches for manual review instead of auto-merging")
	cmd.Flags().StringVar(baseBranch, "base-branch", "", "legacy writable local integration branch used for both assignment base and merge target")
	cmd.Flags().StringVar(baseRef, "base-ref", "", "immutable assignment base ref or commit (default: target branch)")
	cmd.Flags().StringVar(targetBranch, "target-branch", "", "writable local integration branch for merges (default: main)")
	cmd.Flags().BoolVar(mutationTesting, "mutation-testing", false, "run mutation-testing tiers in dispatcher quality gates (off by default)")
	registerWebStartFlags(cmd, webEnabled, noWeb, webAddr)
	registerCleanlinessStartFlags(cmd, cleanliness)
}

func resolvedMaxWorkers(workers, maxWorkers int) int {
	if maxWorkers == 0 {
		return workers
	}
	return maxWorkers
}

func registerWebStartFlags(cmd *cobra.Command, webEnabled, noWeb *bool, webAddr *string) {
	cmd.Flags().BoolVar(webEnabled, "web", true, "enable HTTP server for dashboard/health endpoints")
	cmd.Flags().BoolVar(noWeb, "no-web", false, "disable HTTP server for headless/CI use")
	cmd.Flags().StringVar(webAddr, "web-addr", "", "HTTP server listen address (default 127.0.0.1:4444)")
}

func registerCleanlinessStartFlags(cmd *cobra.Command, cleanliness *cleanlinessStartConfig) {
	cmd.Flags().IntVar(&cleanliness.JanitorInterval, "janitor-interval", cleanliness.JanitorInterval, "run janitor after N completed merges (0 disables janitor)")
	cmd.Flags().IntVar(&cleanliness.JanitorIdleThreshold, "janitor-idle-threshold", cleanliness.JanitorIdleThreshold, "maximum queued tasks before janitor waits")
	cmd.Flags().IntVar(&cleanliness.AuditEveryNJanitors, "audit-every-n-janitors", cleanliness.AuditEveryNJanitors, "run audit every N janitor cycles (0 disables periodic audits)")
	cmd.Flags().IntVar(&cleanliness.JanitorTopK, "janitor-top-k", cleanliness.JanitorTopK, "maximum findings filed per janitor cycle (0 uses natural limit)")
	cmd.Flags().BoolVar(&cleanliness.JanitorEnabled, "janitor-enabled", cleanliness.JanitorEnabled, "enable janitor cycles")
	cmd.Flags().BoolVar(&cleanliness.AuditEnabled, "audit-enabled", cleanliness.AuditEnabled, "enable audit cycles")
}

// startFreshSwarm sets up project env vars and launches the full swarm (daemon + tmux).
func startFreshSwarm(w io.Writer, workers, maxWorkers int, model string, detach bool, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, mutationTesting, webEnabled bool, webAddr string, cleanliness cleanlinessStartConfig) error {
	return startFreshSwarmWithSpawner(w, workers, maxWorkers, model, detach, &ExecDaemonSpawner{
		ProgressTimeout:    progressTimeout,
		OpsReviewTimeout:   opsReviewTimeout,
		ReviewStallTimeout: reviewStallTimeout,
		BaseBranch:         baseBranch,
		ManualIntegration:  manualIntegration,
		MutationTesting:    mutationTesting,
		WebEnabled:         webEnabled,
		WebAddr:            webAddr,
		Cleanliness:        cleanliness,
	})
}

func startFreshSwarmWithBranches(w io.Writer, workers, maxWorkers int, model string, detach bool, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, branches startBranchConfig, mutationTesting, webEnabled bool, webAddr string, cleanliness cleanlinessStartConfig) error {
	return startFreshSwarmWithSpawner(w, workers, maxWorkers, model, detach, &ExecDaemonSpawner{
		ProgressTimeout:    progressTimeout,
		OpsReviewTimeout:   opsReviewTimeout,
		ReviewStallTimeout: reviewStallTimeout,
		BaseRef:            branches.BaseRef,
		TargetBranch:       branches.TargetBranch,
		ManualIntegration:  manualIntegration,
		MutationTesting:    mutationTesting,
		WebEnabled:         webEnabled,
		WebAddr:            webAddr,
		Cleanliness:        cleanliness,
	})
}

func startFreshSwarmWithSpawner(w io.Writer, workers, maxWorkers int, model string, detach bool, spawner *ExecDaemonSpawner) error {
	return withRuntimeProjectEnv(currentRepoRoot(), func(runtimeEnv runtimeProjectEnv) error {
		if err := requireNativeProductionBeadSourceMode("oro start"); err != nil {
			return err
		}
		return runFullStartFn(w, workers, maxWorkers, model, runtimeEnv.Project,
			spawner,
			&ExecRunner{},
			func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) },
			socketPollTimeout, nil, 0, isDetached(detach),
		)
	})
}

func startProjectName(dir string) (string, error) {
	if project := strings.TrimSpace(os.Getenv("ORO_PROJECT")); project != "" {
		return project, nil
	}
	return readProjectConfig(dir)
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
func runDaemonOnly(cmd *cobra.Command, pidPath string, workers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, mutationTesting, webEnabled bool, webAddr string, cleanliness cleanlinessStartConfig) error {
	baseRef, targetBranch := "", ""
	if flag := cmd.Flags().Lookup("base-ref"); flag != nil {
		baseRef, _ = cmd.Flags().GetString("base-ref")
	}
	if flag := cmd.Flags().Lookup("target-branch"); flag != nil {
		targetBranch, _ = cmd.Flags().GetString("target-branch")
	}
	branches, err := resolveStartBranchConfig(cmd.Context(), baseRef, targetBranch, baseBranch)
	if err != nil {
		return err
	}
	return withRuntimeProjectEnv(currentRepoRoot(), func(_ runtimeProjectEnv) error {
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
		d, db, err := buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches(workers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, branches, mutationTesting, webEnabled, webAddr, cleanliness)
		if err != nil {
			return fmt.Errorf("build dispatcher: %w", err)
		}
		defer db.Close()

		wireDependencies(d, paths.SocketPath, paths.OroHome)

		ctx := cmd.Context()
		shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath, d.ShutdownAuthorized())
		defer cleanup()

		if err := d.Run(shutdownCtx); err != nil {
			return fmt.Errorf("dispatcher: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "dispatcher stopped")
		return nil
	})
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
	branches, err := resolveStartBranchConfig(context.Background(), "", "", baseBranch)
	if err != nil {
		return nil, nil, err
	}
	return buildDispatcherWithBranches(branches, webEnabled, webAddr)
}

func buildDispatcherWithBranches(branches startBranchConfig, webEnabled bool, webAddr string) (*dispatcher.Dispatcher, *sql.DB, error) {
	return buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches(1, 1, 0, 0, 0, false, branches, false, webEnabled, webAddr, defaultCleanlinessStartConfig())
}

// buildDispatcherWithReviewTimeouts constructs a Dispatcher with separate
// ops-review subprocess and reviewing-worker stall timeout controls.
func buildDispatcherWithReviewTimeouts(initialWorkers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, mutationTesting, webEnabled bool, webAddr string) (*dispatcher.Dispatcher, *sql.DB, error) { //nolint:funlen,unparam // factory test seam preserves production base-branch parity
	return buildDispatcherWithReviewTimeoutsAndCleanliness(initialWorkers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, baseBranch, mutationTesting, webEnabled, webAddr, defaultCleanlinessStartConfig())
}

func buildDispatcherWithReviewTimeoutsAndCleanliness(initialWorkers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, mutationTesting, webEnabled bool, webAddr string, cleanliness cleanlinessStartConfig) (*dispatcher.Dispatcher, *sql.DB, error) { //nolint:funlen // factory initialization
	branches, err := resolveStartBranchConfig(context.Background(), "", "", baseBranch)
	if err != nil {
		return nil, nil, err
	}
	return buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches(initialWorkers, maxWorkers, progressTimeout, opsReviewTimeout, reviewStallTimeout, manualIntegration, branches, mutationTesting, webEnabled, webAddr, cleanliness)
}

func buildDispatcherWithReviewTimeoutsAndCleanlinessForBranches(initialWorkers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, branches startBranchConfig, mutationTesting, webEnabled bool, webAddr string, cleanliness cleanlinessStartConfig) (*dispatcher.Dispatcher, *sql.DB, error) { //nolint:funlen // factory initialization
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working dir: %w", err)
	}
	if err := verifyStartupRemoteCapabilities(context.Background(), repoRoot); err != nil {
		return nil, nil, err
	}
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
	// Provision the storage catalog, but never let it block boot.
	//
	// The catalog's runtime consumers are the dev-cache sweep below and the
	// file that storage health reports on (factoryhealth
	// evaluateStorageFindings); storage.NewController is never constructed
	// outside tests, so every dispatcher admission gate short-circuits
	// (storage_controller.go:22-24). A failure here previously returned an
	// error and aborted `oro start`. It now warns and continues: degraded
	// storage health belongs in `oro health`, not in a boot failure. The sweep
	// is best-effort for the same reason — cache maintenance must never be the
	// thing that stops the factory starting.
	if catalog, catErr := openStorageCatalog(context.Background(), paths.OroHome); catErr != nil {
		fmt.Fprintf(os.Stderr, "warning: storage catalog unavailable: %v\n", catErr)
	} else {
		runStartupDevCacheSweep(catalog, paths.OroHome)
		if closeErr := catalog.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: close storage catalog: %v\n", closeErr)
		}
	}

	sockPath := paths.SocketPath
	dbPath := paths.StateDBPath
	// Migrate global DBs to per-project directory on first use (no-op if already migrated).
	if project := readProjectNameCWD(); project != "" {
		if err := migrateGlobalDBs(project); err != nil {
			return nil, nil, fmt.Errorf("migrate global DBs: %w", err)
		}
	}

	db, err := openStateDBWithV4Migration(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
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
	esc := dispatcher.NoopEscalator{}
	merger := merge.NewCoordinator(&merge.ExecGitRunner{})
	opsSpawner := ops.NewSpawnerWithReviewTimeout(runtime.opsSpawn, opsReviewTimeout)
	opsSpawner.SetReviewSpawner(runtime.reviewOpsSpawn)

	cfg := dispatcher.Config{
		SocketPath:              sockPath,
		InitialWorkers:          initialWorkers,
		MaxWorkers:              maxWorkers,
		AllowZeroWorkers:        initialWorkers == 0,
		DBPath:                  dbPath,
		RepoRoot:                repoRoot,
		ProgressTimeout:         progressTimeout,
		ReviewTimeout:           reviewStallTimeout,
		ReviewEvidenceDir:       paths.ReviewEvidenceDir,
		ManualIntegration:       manualIntegration,
		MutationTesting:         mutationTesting,
		WorkerProgram:           resolveWorkerProgramPath(repoRoot),
		ReviewPatterns:          resolveReviewPatternsPath(repoRoot),
		ReviewPatternCandidates: resolveReviewPatternCandidatesPath(repoRoot),
		BaseRef:                 branches.BaseRef,
		TargetBranch:            branches.TargetBranch,
		DefaultBranch:           branches.TargetBranch,
		DreamInterval:           10,
		JanitorInterval:         cleanliness.JanitorInterval,
		JanitorIdleThreshold:    cleanliness.JanitorIdleThreshold,
		AuditEveryNJanitors:     cleanliness.AuditEveryNJanitors,
		JanitorTopK:             cleanliness.JanitorTopK,
		JanitorEnabled:          cleanliness.JanitorEnabled,
		AuditEnabled:            cleanliness.AuditEnabled,
		WebEnabled:              webEnabled,
		WebAddr:                 webAddr,
		StorageHealth: func(ctx context.Context) *factoryhealth.StorageHealth {
			return loadFactoryStorageHealth(ctx, paths.OroHome)
		},
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, codeIdx,
		dispatcher.WithMemoryServices(newDispatcherMemoryServices(db)))
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

// resolveReviewPatternsPath returns the review-patterns path for repoRoot.
// Falls back to <repoRoot>/assets/review-patterns.md if path resolution fails.
func resolveReviewPatternsPath(repoRoot string) string {
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		return filepath.Join(repoRoot, "assets", "review-patterns.md")
	}
	return paths.ReviewPatterns
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
func wireDependencies(d *dispatcher.Dispatcher, sockPath, oroHome string) {
	d.SetProcessManager(dispatcher.NewOroProcessManager(sockPath, oroHome))
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

// probeDispatcherState connects to the dispatcher socket, sends a status
// directive, and returns the dispatcher's current state string (e.g. "inert",
// "running", "paused"). Returns an error if the socket is unreachable or the
// response cannot be parsed.
func probeDispatcherState(sockPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialDispatcher(ctx, sockPath)
	if err != nil {
		return "", fmt.Errorf("dial dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := sendDirective(conn, "status", ""); err != nil {
		return "", fmt.Errorf("send status directive: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}
	ack, err := readACK(conn)
	if err != nil {
		return "", fmt.Errorf("read ack: %w", err)
	}
	resp, err := parseStatusFromACK(ack.Detail)
	if err != nil {
		return "", fmt.Errorf("parse status: %w", err)
	}
	return resp.State, nil
}

// sendScaleDirective connects to the dispatcher socket and sends a scale
// directive to set the target worker pool size to workers.
func sendScaleDirective(sockPath string, workers int) error {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", sockPath)
	if err != nil {
		return fmt.Errorf("connect to dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := sendDirective(conn, "scale", strconv.Itoa(workers)); err != nil {
		return fmt.Errorf("send scale directive: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	return nil
}

// activateInertDispatcher probes the dispatcher state and, if inert, sends a
// start directive to transition from inert→running, then sends a scale
// directive to apply the requested worker count. No-op when the dispatcher is
// already in a non-inert state.
func activateInertDispatcher(sockPath string, workers int) error {
	state, err := probeDispatcherState(sockPath)
	if err != nil {
		return fmt.Errorf("probe dispatcher state: %w", err)
	}
	if state != "inert" {
		return nil
	}
	if err := sendStartDirective(sockPath); err != nil {
		return fmt.Errorf("activate inert dispatcher: %w", err)
	}
	return sendScaleDirective(sockPath, workers)
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
