package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oro/pkg/processenv"
)

const (
	residualCleanupTimeout = 4 * time.Second
	residualTERMGrace      = 250 * time.Millisecond
	residualScanBatchSize  = 128
)

type processEnvironmentSnapshots struct {
	environments map[int][]string
}

type processEnvironmentBatchReader func(context.Context, []OwnedProcess) (processEnvironmentSnapshots, error)

// OwnedProcess identifies a detached process and its process group.
//
//oro:testonly
type OwnedProcess struct {
	PID  int
	PGID int
}

func (pm *ExecProcessManager) cleanupResidualProcesses(id string) error {
	markers := processenv.WorkerOwnershipMarkers(pm.socketPath, id)
	if len(markers) == 0 || pm.residualScanFn == nil || pm.residualKillFn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), residualCleanupTimeout)
	defer cancel()
	processes, err := pm.residualScanFn(ctx, markers)
	if err != nil {
		return fmt.Errorf("scan detached processes for worker %s: %w", id, err)
	}
	if len(processes) == 0 {
		return nil
	}
	if err := pm.residualKillFn(ctx, processes...); err != nil {
		return fmt.Errorf("kill detached processes for worker %s: %w", id, err)
	}
	return nil
}

func scanOwnedProcesses(ctx context.Context, markers []string) ([]OwnedProcess, error) {
	if !hasCompleteOwnershipMarkers(markers) {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "ps", "x", "-o", "pid=,pgid=").Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("list process IDs: %w", ctxErr)
		}
		return nil, fmt.Errorf("list process IDs: %w", err)
	}
	return inspectProcessEnvironments(ctx, processesFromSnapshot(string(out)), markers)
}

func processesFromSnapshot(snapshot string) []OwnedProcess {
	self := os.Getpid()
	var processes []OwnedProcess
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, pgidErr := strconv.Atoi(fields[1])
		if pidErr != nil || pgidErr != nil || pid <= 1 || pid == self {
			continue
		}
		processes = append(processes, OwnedProcess{PID: pid, PGID: pgid})
	}
	return processes
}

func inspectProcessEnvironments(ctx context.Context, processes []OwnedProcess, markers []string) ([]OwnedProcess, error) {
	return inspectProcessEnvironmentsWithReader(ctx, processes, markers, readProcessEnvironmentSnapshots)
}

func inspectProcessEnvironmentsWithReader(
	ctx context.Context,
	processes []OwnedProcess,
	markers []string,
	readBatch processEnvironmentBatchReader,
) ([]OwnedProcess, error) {
	if !hasCompleteOwnershipMarkers(markers) {
		return nil, nil
	}
	processes = uniqueOwnedProcesses(processes)
	if len(processes) == 0 {
		return nil, nil
	}

	owned := make([]OwnedProcess, 0)
	for start := 0; start < len(processes); start += residualScanBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("inspect process environments: %w", err)
		}
		end := min(start+residualScanBatchSize, len(processes))
		batch := processes[start:end]
		snapshots, err := readBatch(ctx, batch)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("inspect process environments: %w", ctxErr)
			}
			if allOwnedProcessesExited(ctx, batch) {
				continue
			}
			return nil, fmt.Errorf("inspect process environments: %w", err)
		}
		owned = append(owned, ownedProcessesFromEnvironmentSnapshot(batch, snapshots.environments, markers)...)
	}
	return uniqueOwnedProcesses(owned), nil
}

func readProcessEnvironmentSnapshots(ctx context.Context, processes []OwnedProcess) (processEnvironmentSnapshots, error) {
	return readProcessEnvironmentSnapshotsWithReader(ctx, processes, processenv.ReadEntries)
}

func readProcessEnvironmentSnapshotsWithReader(
	ctx context.Context,
	processes []OwnedProcess,
	readEntries func(int) ([]string, error),
) (processEnvironmentSnapshots, error) {
	environments := make(map[int][]string, len(processes))
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return processEnvironmentSnapshots{}, fmt.Errorf("read process environment entries: %w", err)
		}
		entries, err := readEntries(process.PID)
		if err == nil {
			if len(entries) > 0 {
				environments[process.PID] = entries
			}
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return processEnvironmentSnapshots{}, fmt.Errorf("read process environment entries: %w", ctxErr)
		}
		if errors.Is(err, os.ErrPermission) {
			continue
		}
		if errors.Is(err, syscall.EINVAL) {
			continue
		}
		if errors.Is(err, syscall.EIO) {
			continue
		}
		if allOwnedProcessesExited(ctx, []OwnedProcess{process}) {
			continue
		}
		if ownedProcessDefinitelyForeign(ctx, process.PID) {
			continue
		}
		return processEnvironmentSnapshots{}, fmt.Errorf("read process environment entries for pid %d: %w", process.PID, err)
	}
	return processEnvironmentSnapshots{environments: environments}, nil
}

func ownedProcessesFromEnvironmentSnapshot(processes []OwnedProcess, environments map[int][]string, markers []string) []OwnedProcess {
	owned := make([]OwnedProcess, 0)
	for _, process := range processes {
		if !processenv.CommandContainsAllMarkers(environments[process.PID], markers) {
			continue
		}
		owned = append(owned, process)
	}
	return owned
}

func hasCompleteOwnershipMarkers(markers []string) bool {
	if len(markers) != 2 {
		return false
	}
	var socket, worker bool
	for _, marker := range markers {
		switch {
		case strings.HasPrefix(marker, processenv.SocketPathEnv+"=") && len(marker) > len(processenv.SocketPathEnv)+1:
			socket = true
		case strings.HasPrefix(marker, processenv.WorkerIDEnv+"=") && len(marker) > len(processenv.WorkerIDEnv)+1:
			worker = true
		default:
			return false
		}
	}
	return socket && worker
}

func killOwnedProcesses(ctx context.Context, processes ...OwnedProcess) error {
	processes = uniqueOwnedProcesses(processes)
	signalOwnedProcesses(processes, syscall.SIGTERM)
	if waitForOwnedProcesses(ctx, processes, residualTERMGrace) {
		return nil
	}
	signalOwnedProcesses(processes, syscall.SIGKILL)
	if waitForOwnedProcesses(ctx, processes, residualCleanupTimeout) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wait for owned processes: %w", err)
	}
	return fmt.Errorf("owned processes did not exit before cleanup timeout")
}

func uniqueOwnedProcesses(processes []OwnedProcess) []OwnedProcess {
	seen := make(map[int]bool)
	unique := make([]OwnedProcess, 0, len(processes))
	for _, process := range processes {
		if process.PID <= 1 || seen[process.PID] {
			continue
		}
		seen[process.PID] = true
		unique = append(unique, process)
	}
	return unique
}

func signalOwnedProcesses(processes []OwnedProcess, signal syscall.Signal) {
	selfPGID, _ := syscall.Getpgid(0)
	seenGroups := make(map[int]bool)
	for _, process := range processes {
		if process.PGID > 1 && process.PGID != selfPGID && !seenGroups[process.PGID] {
			_ = syscall.Kill(-process.PGID, signal)
			seenGroups[process.PGID] = true
		}
		_ = syscall.Kill(process.PID, signal)
	}
}

func waitForOwnedProcesses(ctx context.Context, processes []OwnedProcess, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if allOwnedProcessesExited(ctx, processes) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func allOwnedProcessesExited(ctx context.Context, processes []OwnedProcess) bool {
	for _, process := range processes {
		if !ownedProcessExited(ctx, process.PID) {
			return false
		}
	}
	return true
}

func ownedProcessExited(ctx context.Context, pid int) bool {
	return ownedProcessExitedWithProbe(ctx, pid, func(pid int) error {
		return syscall.Kill(pid, syscall.Signal(0))
	}, func(ctx context.Context, pid int) (string, error) {
		out, err := exec.CommandContext(ctx, "ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is numeric process metadata
		return string(out), err
	})
}

func ownedProcessExitedWithProbe(ctx context.Context, pid int, signalZero func(int) error, processState func(context.Context, int) (string, error)) bool {
	if err := signalZero(pid); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	state, err := processState(ctx, pid)
	if err == nil {
		return strings.HasPrefix(strings.TrimSpace(state), "Z")
	}
	return errors.Is(signalZero(pid), syscall.ESRCH)
}

func ownedProcessDefinitelyForeign(ctx context.Context, pid int) bool {
	uidOutput, err := exec.CommandContext(ctx, "ps", "-o", "uid=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // pid is numeric process metadata
	if err != nil {
		return false
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(uidOutput)))
	return err == nil && uid != os.Geteuid()
}
