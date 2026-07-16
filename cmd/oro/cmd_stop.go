package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"oro/pkg/processenv"
)

// stopConfig holds injectable dependencies for the graceful shutdown sequence.
type stopConfig struct {
	pidPath         string
	sockPath        string
	tmuxName        string
	runner          CmdRunner
	w               io.Writer
	stdin           io.Reader       // stdin for interactive confirmation
	signalFn        func(int) error // sends SIGINT; injectable for testing
	aliveFn         func(int) bool  // checks process liveness; injectable for testing
	killFn          func(int) error // sends SIGKILL; injectable for testing
	treeKillFn      func(context.Context, int, []string) error
	residualScanFn  func(context.Context, []string, []string) ([]ResidualProcess, error)
	residualKillFn  func(context.Context, ...ResidualProcess) error
	isTTY           func() bool // returns true if stdin is a TTY; injectable for testing
	force           bool        // --force flag: skip interactive confirmation
	oroHome         string      // base directory for daemon discovery
	stateDBPath     string
	residualRoots   []string
	residualMarkers []string
}

// projectDaemon describes a running daemon discovered in a project directory.
type projectDaemon struct {
	Project string // project name or "(global)" for legacy
	PID     int
	PIDPath string
}

// discoverProjectDaemons scans oroHome/projects/*/oro.pid for running daemons.
// Also checks the legacy global oroHome/oro.pid.
func discoverProjectDaemons(oroHome string) []projectDaemon {
	var daemons []projectDaemon

	// Check legacy global PID file.
	globalPID := filepath.Join(oroHome, "oro.pid")
	if pid, err := ReadPIDFile(globalPID); err == nil && IsProcessAlive(pid) {
		daemons = append(daemons, projectDaemon{
			Project: "(global)",
			PID:     pid,
			PIDPath: globalPID,
		})
	}

	// Scan per-project PID files.
	projectsDir := filepath.Join(oroHome, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return daemons // projects dir doesn't exist yet
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pidPath := filepath.Join(projectsDir, e.Name(), "oro.pid")
		pid, err := ReadPIDFile(pidPath)
		if err != nil {
			continue
		}
		if !IsProcessAlive(pid) {
			continue
		}
		daemons = append(daemons, projectDaemon{
			Project: e.Name(),
			PID:     pid,
			PIDPath: pidPath,
		})
	}

	return daemons
}

// drainTimeout is how long to wait for the dispatcher to exit after SIGTERM.
const drainTimeout = 30 * time.Second

// drainPollInterval is how often to check if the dispatcher has exited.
const drainPollInterval = 200 * time.Millisecond

// isStdinTTY returns true if os.Stdin is connected to a terminal.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// newStopCmd creates the "oro stop" subcommand.
func newStopCmd() *cobra.Command {
	var (
		force bool
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Graceful shutdown of the Oro swarm",
		Long: `Sends a stop directive to the dispatcher, waits for workers to finish,
and kills the tmux session.

Use --all to stop daemons in all projects simultaneously.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			if all {
				return runStopAll(cmd.Context(), paths.OroHome, force, cmd.OutOrStdout())
			}

			projectName := readProjectNameCWD()
			cfg := &stopConfig{
				pidPath:         paths.PIDPath,
				sockPath:        paths.SocketPath,
				tmuxName:        TmuxSessionName(projectName),
				runner:          &ExecRunner{},
				w:               cmd.OutOrStdout(),
				stdin:           os.Stdin,
				signalFn:        defaultSignalINT,
				aliveFn:         IsProcessAlive,
				killFn:          defaultKill,
				treeKillFn:      killProcessTree,
				residualScanFn:  scanOroResidualProcesses,
				residualKillFn:  killResidualProcess,
				isTTY:           isStdinTTY,
				force:           force,
				oroHome:         paths.OroHome,
				stateDBPath:     paths.StateDBPath,
				residualRoots:   stopResidualRoots(paths.PIDPath),
				residualMarkers: defaultOroResidualMarkers(projectName, paths.SocketPath),
			}

			return runStopSequence(cmd.Context(), cfg)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation (requires ORO_HUMAN_CONFIRMED=1)")
	cmd.Flags().BoolVar(&all, "all", false, "stop daemons in all projects")
	return cmd
}

// suggestStopAll prints a hint about other running daemons when the current
// project's daemon is not running.
func suggestStopAll(w io.Writer, oroHome string) {
	if oroHome == "" {
		return
	}
	others := discoverProjectDaemons(oroHome)
	if len(others) == 0 {
		return
	}
	fmt.Fprintf(w, "\nFound %d running daemon(s) in other projects:\n", len(others))
	for _, d := range others {
		fmt.Fprintf(w, "  - %s (PID %d)\n", d.Project, d.PID)
	}
	fmt.Fprintln(w, "\nUse 'oro stop --all' to stop all daemons.")
}

// runStopAll discovers and stops all running project daemons.
func runStopAll(ctx context.Context, oroHome string, force bool, w io.Writer) error {
	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) == 0 {
		fmt.Fprintln(w, "no running daemons found")
		return nil
	}

	fmt.Fprintf(w, "found %d running daemon(s):\n", len(daemons))
	for _, d := range daemons {
		fmt.Fprintf(w, "  - %s (PID %d)\n", d.Project, d.PID)
	}

	for _, d := range daemons {
		sockPath := strings.TrimSuffix(d.PIDPath, "oro.pid") + "oro.sock"
		projectName := d.Project
		if projectName == "(global)" {
			projectName = ""
		}

		cfg := &stopConfig{
			pidPath:         d.PIDPath,
			sockPath:        sockPath,
			tmuxName:        TmuxSessionName(d.Project),
			runner:          &ExecRunner{},
			w:               w,
			stdin:           os.Stdin,
			signalFn:        defaultSignalINT,
			aliveFn:         IsProcessAlive,
			killFn:          defaultKill,
			treeKillFn:      killProcessTree,
			residualScanFn:  scanOroResidualProcesses,
			residualKillFn:  killResidualProcess,
			isTTY:           isStdinTTY,
			force:           force,
			stateDBPath:     strings.TrimSuffix(d.PIDPath, "oro.pid") + "state.db",
			residualRoots:   []string{filepath.Dir(d.PIDPath)},
			residualMarkers: defaultOroResidualMarkers(projectName, sockPath),
		}

		fmt.Fprintf(w, "\nstopping %s (PID %d)...\n", d.Project, d.PID)
		if err := runStopSequence(ctx, cfg); err != nil {
			fmt.Fprintf(w, "warning: failed to stop %s: %v\n", d.Project, err)
		}
	}
	return nil
}

// defaultSignalINT sends SIGINT to the given PID.
// SIGINT is always honored by the daemon (like Ctrl+C), unlike SIGTERM which
// requires prior authorization via shutdown directive. This avoids the UDS
// directive path which agents can abuse.
func defaultSignalINT(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		return fmt.Errorf("send SIGINT to PID %d: %w", pid, err)
	}
	return nil
}

// defaultKill sends SIGKILL to the given PID.
func defaultKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("send SIGKILL to PID %d: %w", pid, err)
	}
	return nil
}

// confirmStop checks that the caller is authorized to stop the dispatcher.
// In interactive mode, it prompts for "YES" on stdin.
// With --force, it requires ORO_HUMAN_CONFIRMED=1.
// Returns an error if confirmation fails.
func confirmStop(cfg *stopConfig) error {
	if cfg.force {
		if os.Getenv("ORO_HUMAN_CONFIRMED") != "1" {
			return fmt.Errorf("--force requires ORO_HUMAN_CONFIRMED=1 environment variable")
		}
		return nil
	}

	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro stop requires an interactive terminal (stdin is not a TTY)\n" +
			"Hint: use --force with ORO_HUMAN_CONFIRMED=1 for non-interactive use")
	}

	fmt.Fprint(cfg.w, "Type YES to confirm shutdown: ")
	scanner := bufio.NewScanner(cfg.stdin)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read confirmation from stdin")
	}
	if strings.TrimSpace(scanner.Text()) != "YES" {
		return fmt.Errorf("shutdown aborted (expected YES)")
	}
	return nil
}

// runStopSequence performs the full graceful shutdown:
//  0. Confirm the caller is authorized (interactive TTY or --force)
//  1. Send SIGINT to the dispatcher (always honored, triggers graceful drain)
//  2. Wait for the dispatcher process to exit
//  3. If process won't exit: SIGKILL as emergency fallback
//  4. Clean up pane-died hooks
//  5. Kill the tmux session
//  6. Remove PID file
func runStopSequence(ctx context.Context, cfg *stopConfig) error {
	status, pid, err := DaemonStatus(cfg.pidPath, cfg.sockPath)
	if err != nil {
		return fmt.Errorf("get daemon status: %w", err)
	}

	switch status {
	case StatusStopped:
		fmt.Fprintln(cfg.w, "dispatcher is not running")
		suggestStopAll(cfg.w, cfg.oroHome)
		return nil
	case StatusStale:
		fmt.Fprintln(cfg.w, "removing stale PID file (process already dead)")
		_ = os.Remove(cfg.sockPath)
		return RemovePIDFile(cfg.pidPath)
	}

	// 0. Confirm authorization before proceeding.
	if err := confirmStop(cfg); err != nil {
		return err
	}

	// 1. Send SIGINT (always honored by daemon, like Ctrl+C).
	fmt.Fprintf(cfg.w, "sending SIGINT to dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: SIGINT failed: %v\n", err)
	}

	// 2. Wait for the dispatcher to exit.
	fmt.Fprintln(cfg.w, "waiting for dispatcher to drain and exit...")
	if err := waitForExit(ctx, pid, cfg.aliveFn); err != nil {
		fmt.Fprintf(cfg.w, "warning: %v\n", err)
		killDispatcherAfterTimeout(cfg, pid)
	}

	// 4. Clean up pane-died hooks before killing the tmux session.
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	_ = tmux.CleanupPaneDiedHooks() // Best effort; non-fatal if hooks weren't registered

	// 5. Kill the tmux session.
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}

	// 6. Sweep escaped/reparented Oro-owned descendants after the primary
	// process tree and tmux session have been stopped.
	scanCtx, cancel := context.WithTimeout(context.Background(), 2*processKillGracePeriod)
	scanAndKillOroResiduals(scanCtx, cfg)
	cancel()

	// 7. Remove PID file (belt and suspenders — signal handler may have already done it).
	_ = RemovePIDFile(cfg.pidPath)

	fmt.Fprintln(cfg.w, "shutdown complete")
	return nil
}

func killDispatcherAfterTimeout(cfg *stopConfig, pid int) {
	if cfg.treeKillFn != nil {
		fmt.Fprintf(cfg.w, "killing dispatcher process tree (PID %d)\n", pid)
		killCtx, cancel := context.WithTimeout(context.Background(), 2*processKillGracePeriod)
		defer cancel()
		patterns := cfg.residualMarkers
		if len(patterns) == 0 {
			patterns = defaultOroResidualMarkers("", cfg.sockPath)
		}
		if killErr := cfg.treeKillFn(killCtx, pid, patterns); killErr != nil {
			fmt.Fprintf(cfg.w, "warning: process tree kill failed: %v\n", killErr)
		}
		return
	}
	if cfg.killFn == nil {
		return
	}
	fmt.Fprintf(cfg.w, "sending SIGKILL to dispatcher (PID %d)\n", pid)
	if killErr := cfg.killFn(pid); killErr != nil {
		fmt.Fprintf(cfg.w, "warning: SIGKILL failed: %v\n", killErr)
	}
}

func scanAndKillOroResiduals(ctx context.Context, cfg *stopConfig) {
	if cfg.residualScanFn == nil || cfg.residualKillFn == nil {
		return
	}
	roots := cfg.residualRoots
	if len(roots) == 0 && cfg.pidPath != "" {
		roots = []string{filepath.Dir(cfg.pidPath)}
	}
	if cfg.stateDBPath != "" {
		activeRoots, err := activeAssignmentWorktreeRoots(ctx, cfg.stateDBPath)
		if err != nil {
			fmt.Fprintf(cfg.w, "warning: active assignment root scan failed: %v\n", err)
		}
		roots = append(roots, activeRoots...)
	}
	roots = uniqueResidualRoots(roots)
	markers := cfg.residualMarkers
	if len(markers) == 0 {
		markers = defaultOroResidualMarkers("", cfg.sockPath)
	}
	residuals, err := cfg.residualScanFn(ctx, roots, markers)
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: residual process scan failed: %v\n", err)
		return
	}
	for _, residual := range residuals {
		fmt.Fprintf(cfg.w, "killing residual Oro process PID %d (%s)\n", residual.PID, residual.Evidence)
	}
	if err := cfg.residualKillFn(ctx, residuals...); err != nil {
		fmt.Fprintf(cfg.w, "warning: kill residual processes: %v\n", err)
	}
}

func stopResidualRoots(pidPath string) []string {
	roots := []string{filepath.Dir(pidPath)}
	cwd, err := os.Getwd()
	if err != nil {
		return uniqueResidualRoots(roots)
	}
	projectPaths, err := ResolvePaths(cwd)
	if err == nil && projectPaths.WorktreesDir != "" {
		roots = append(roots, projectPaths.WorktreesDir)
	}
	return uniqueResidualRoots(roots)
}

func activeAssignmentWorktreeRoots(ctx context.Context, stateDBPath string) ([]string, error) {
	if stateDBPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(stateDBPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat state db: %w", err)
	}
	db, err := openDB(stateDBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT worktree FROM assignments WHERE status='active' AND worktree <> ''`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query active assignment worktrees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var roots []string
	for rows.Next() {
		var worktree string
		if err := rows.Scan(&worktree); err != nil {
			return nil, fmt.Errorf("scan active assignment worktree: %w", err)
		}
		roots = append(roots, worktree)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active assignment worktrees: %w", err)
	}
	return uniqueResidualRoots(roots), nil
}

func uniqueResidualRoots(roots []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		unique = append(unique, root)
	}
	return unique
}

// waitForExit polls until the process is no longer alive or timeout.
func waitForExit(ctx context.Context, pid int, aliveFn func(int) bool) error {
	if !aliveFn(pid) {
		return nil
	}

	deadline := time.After(drainTimeout)
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !aliveFn(pid) {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("timeout waiting for dispatcher (PID %d) to exit", pid)
		case <-ctx.Done():
			return fmt.Errorf("wait for dispatcher exit: %w", ctx.Err())
		}
	}
}

// ResidualProcess is an escaped or reparented process with Oro ownership
// evidence strong enough for stop/cleanup to terminate it.
type ResidualProcess struct {
	PID      int
	PPID     int
	PGID     int
	Session  int
	Command  string
	Evidence string
}

type processSnapshot struct {
	PID         int
	PPID        int
	PGID        int
	Session     int
	Command     string
	Environment string
}

func defaultOroResidualMarkers(_, sockPath string) []string {
	if sockPath == "" {
		return nil
	}
	return []string{"ORO_SOCKET_PATH=" + sockPath}
}

// killProcessTree terminates pid's process group and descendants. Missing
// processes are treated as success so stop remains idempotent.
func killProcessTree(ctx context.Context, pid int, patterns []string) error {
	if pid <= 1 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(syscall.Signal(0))
	}
	descendants := descendantPIDs(ctx, pid)
	processGroups := descendantProcessGroups(descendants)
	_ = killProcessGroup(pid, syscall.SIGTERM)
	for _, pgid := range processGroups {
		_ = killProcessGroupID(pgid, syscall.SIGTERM)
	}
	for _, child := range descendants {
		_ = syscall.Kill(child, syscall.SIGTERM)
	}
	timer := time.NewTimer(processKillGracePeriod)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
	}
	_ = killProcessGroup(pid, syscall.SIGKILL)
	for _, pgid := range processGroups {
		_ = killProcessGroupID(pgid, syscall.SIGKILL)
	}
	for _, child := range descendants {
		_ = syscall.Kill(child, syscall.SIGKILL)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !isNoSuchProcess(err) {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	_ = patterns
	return nil
}

func killProcessGroup(pid int, sig syscall.Signal) error {
	pgid, err := processGroupID(pid)
	if err != nil || pgid <= 1 {
		return err
	}
	return killProcessGroupID(pgid, sig)
}

func killProcessGroupID(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return nil
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !isNoSuchProcess(err) {
		return fmt.Errorf("kill process group %d: %w", pgid, err)
	}
	return nil
}

func processGroupID(pid int) (int, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is numeric process metadata
	if err != nil {
		return 0, fmt.Errorf("lookup pgid for pid %d: %w", pid, err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse pgid for pid %d: %w", pid, err)
	}
	return pgid, nil
}

func descendantPIDs(ctx context.Context, pid int) []int {
	out, err := exec.CommandContext(ctx, "pgrep", "-P", strconv.Itoa(pid)).Output() //nolint:gosec // pid is numeric process metadata
	if err != nil {
		return nil
	}
	var descendants []int
	for _, field := range strings.Fields(string(out)) {
		child, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		descendants = append(descendants, descendantPIDs(ctx, child)...)
		descendants = append(descendants, child)
	}
	return descendants
}

func descendantProcessGroups(pids []int) []int {
	seen := make(map[int]bool)
	var groups []int
	for _, pid := range pids {
		pgid, err := processGroupID(pid)
		if err != nil || pgid <= 1 || seen[pgid] {
			continue
		}
		seen[pgid] = true
		groups = append(groups, pgid)
	}
	return groups
}

// scanOroResidualProcesses returns only processes with Oro ownership evidence:
// a known project/worktree root in the command line or explicit Oro markers.
func scanOroResidualProcesses(ctx context.Context, roots, markers []string) ([]ResidualProcess, error) {
	snapshots, err := defaultProcessSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	return scanOroResidualProcessSnapshots(snapshots, roots, markers), nil
}

func scanOroResidualProcessSnapshots(snapshots []processSnapshot, roots, markers []string) []ResidualProcess {
	self := os.Getpid()
	var residuals []ResidualProcess
	for _, proc := range snapshots {
		if proc.PID <= 1 || proc.PID == self {
			continue
		}
		evidence := residualEvidence(proc.Command, proc.Environment, roots, markers)
		if evidence == "" {
			continue
		}
		residuals = append(residuals, ResidualProcess{
			PID: proc.PID, PPID: proc.PPID, PGID: proc.PGID, Session: proc.Session,
			Command: proc.Command, Evidence: evidence,
		})
	}
	return residuals
}

func residualEvidence(command, environment string, roots, markers []string) string {
	if len(markers) > 0 {
		if processenv.CommandContainsAllMarkers(environment, markers) {
			return "markers:" + strings.Join(markers, ",")
		}
		return ""
	}
	for _, root := range roots {
		if root != "" && commandContainsRoot(command, root) {
			return "root:" + filepath.Clean(root)
		}
	}
	return ""
}

func commandContainsRoot(command, root string) bool {
	root = filepath.Clean(root)
	for start := 0; ; {
		idx := strings.Index(command[start:], root)
		if idx < 0 {
			return false
		}
		idx += start
		end := idx + len(root)
		if commandBoundaryBefore(command, idx) && rootBoundaryAfter(command, end) {
			return true
		}
		start = idx + 1
	}
}

func commandBoundaryBefore(s string, idx int) bool {
	if idx <= 0 {
		return true
	}
	return isCommandBoundary(s[idx-1])
}

func rootBoundaryAfter(s string, idx int) bool {
	if idx >= len(s) {
		return true
	}
	return s[idx] == filepath.Separator || isCommandBoundary(s[idx])
}

func isCommandBoundary(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' ||
		b == '\'' || b == '"' || b == '`' ||
		b == '=' || b == ':' || b == ',' || b == ';' ||
		b == ')' || b == '(' || b == '[' || b == ']'
}

func defaultProcessSnapshots(ctx context.Context) ([]processSnapshot, error) {
	commandOut, err := exec.CommandContext(ctx, "ps", "axww", "-o", "pid=,ppid=,pgid=,sess=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("list process commands: %w", err)
	}
	environmentOut, err := exec.CommandContext(ctx, "ps", "axeww", "-o", "pid=,ppid=,pgid=,sess=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("list process environments: %w", err)
	}
	return processSnapshotsFromOutputs(string(commandOut), string(environmentOut)), nil
}

func processSnapshotsFromOutputs(commandOutput, environmentOutput string) []processSnapshot {
	snapshots := parseProcessSnapshots(commandOutput)
	indexByPID := make(map[int]int, len(snapshots))
	for index, snapshot := range snapshots {
		indexByPID[snapshot.PID] = index
	}
	for _, combined := range parseProcessSnapshots(environmentOutput) {
		index, ok := indexByPID[combined.PID]
		if !ok {
			continue
		}
		environment, separated := stopProcessEnvironmentSuffix(combined.Command, snapshots[index].Command)
		if separated {
			snapshots[index].Environment = environment
		}
	}
	return snapshots
}

func parseProcessSnapshots(output string) []processSnapshot {
	snapshots := make([]processSnapshot, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		pgid, pgidErr := strconv.Atoi(fields[2])
		sess, sessErr := strconv.Atoi(fields[3])
		if pidErr != nil || ppidErr != nil || pgidErr != nil || sessErr != nil {
			continue
		}
		snapshots = append(snapshots, processSnapshot{
			PID: pid, PPID: ppid, PGID: pgid, Session: sess,
			Command: strings.Join(fields[4:], " "),
		})
	}
	return snapshots
}

func stopProcessEnvironmentSuffix(commandAndEnvironment, command string) (string, bool) {
	if command == "" || !strings.HasPrefix(commandAndEnvironment, command) {
		return "", false
	}
	suffix := commandAndEnvironment[len(command):]
	if suffix == "" || (suffix[0] != ' ' && suffix[0] != '\t') {
		return "", false
	}
	return strings.TrimSpace(suffix), true
}

func killResidualProcess(ctx context.Context, residuals ...ResidualProcess) error {
	pids, pgids := uniqueResidualTargets(residuals)
	for _, pgid := range pgids {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	timer := time.NewTimer(processKillGracePeriod)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
	}
	var firstErr error
	for _, pgid := range pgids {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !isNoSuchProcess(err) && firstErr == nil {
			firstErr = fmt.Errorf("kill residual process group %d: %w", pgid, err)
		}
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !isNoSuchProcess(err) && firstErr == nil {
			firstErr = fmt.Errorf("kill residual pid %d: %w", pid, err)
		}
	}
	return firstErr
}

func uniqueResidualTargets(residuals []ResidualProcess) (pids, pgids []int) {
	seenPIDs := make(map[int]bool)
	seenPGIDs := make(map[int]bool)
	for _, residual := range residuals {
		if residual.PGID > 1 && !seenPGIDs[residual.PGID] {
			seenPGIDs[residual.PGID] = true
			pgids = append(pgids, residual.PGID)
		}
		if residual.PID > 1 && !seenPIDs[residual.PID] {
			seenPIDs[residual.PID] = true
			pids = append(pids, residual.PID)
		}
	}
	return pids, pgids
}

func isNoSuchProcess(err error) bool {
	return errors.Is(err, syscall.ESRCH) || strings.Contains(strings.ToLower(err.Error()), "no such process")
}
