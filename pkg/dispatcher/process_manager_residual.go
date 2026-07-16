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
	commands     string
	environments string
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
			if allOwnedProcessesExited(batch) {
				continue
			}
			return nil, fmt.Errorf("inspect process environments: %w", err)
		}
		commands := processCommandsFromSnapshot(snapshots.commands)
		owned = append(owned, ownedProcessesFromEnvironmentSnapshot(snapshots.environments, commands, markers)...)
	}
	return uniqueOwnedProcesses(owned), nil
}

func readProcessEnvironmentSnapshots(ctx context.Context, processes []OwnedProcess) (processEnvironmentSnapshots, error) {
	pids := make([]string, 0, len(processes))
	for _, process := range processes {
		pids = append(pids, strconv.Itoa(process.PID))
	}
	pidList := strings.Join(pids, ",")
	//nolint:gosec // PID arguments are parsed integers from the local ps snapshot.
	commandOut, err := exec.CommandContext(ctx, "ps", "ww", "-p", pidList, "-o", "pid=,command=").Output()
	if err != nil {
		return processEnvironmentSnapshots{}, fmt.Errorf("list process commands: %w", err)
	}
	//nolint:gosec // PID arguments are parsed integers from the local ps snapshot.
	environmentOut, err := exec.CommandContext(ctx, "ps", "eww", "-p", pidList, "-o", "pid=,pgid=,command=").Output()
	if err != nil {
		return processEnvironmentSnapshots{}, fmt.Errorf("list process environments: %w", err)
	}
	return processEnvironmentSnapshots{commands: string(commandOut), environments: string(environmentOut)}, nil
}

func processCommandsFromSnapshot(snapshot string) map[int]string {
	commands := make(map[int]string)
	for _, line := range strings.Split(snapshot, "\n") {
		pidField, command, ok := splitProcessField(line)
		pid, err := strconv.Atoi(pidField)
		if !ok || err != nil {
			continue
		}
		commands[pid] = command
	}
	return commands
}

func ownedProcessesFromEnvironmentSnapshot(snapshot string, commands map[int]string, markers []string) []OwnedProcess {
	owned := make([]OwnedProcess, 0)
	for _, line := range strings.Split(snapshot, "\n") {
		pidField, remainder, ok := splitProcessField(line)
		if !ok {
			continue
		}
		pgidField, commandAndEnvironment, ok := splitProcessField(remainder)
		pid, pidErr := strconv.Atoi(pidField)
		pgid, pgidErr := strconv.Atoi(pgidField)
		command, candidate := commands[pid]
		environment, separated := processEnvironmentSuffix(commandAndEnvironment, command)
		if !ok || pidErr != nil || pgidErr != nil || !candidate || !separated ||
			!processenv.CommandContainsAllMarkers(environment, markers) {
			continue
		}
		owned = append(owned, OwnedProcess{PID: pid, PGID: pgid})
	}
	return owned
}

func splitProcessField(line string) (field, remainder string, ok bool) {
	line = strings.TrimSpace(line)
	separator := strings.IndexAny(line, " \t")
	if separator <= 0 {
		return "", "", false
	}
	remainder = strings.TrimLeft(line[separator:], " \t")
	if remainder == "" {
		return "", "", false
	}
	return line[:separator], remainder, true
}

func processEnvironmentSuffix(commandAndEnvironment, command string) (string, bool) {
	if command == "" || !strings.HasPrefix(commandAndEnvironment, command) {
		return "", false
	}
	suffix := commandAndEnvironment[len(command):]
	if suffix == "" || (suffix[0] != ' ' && suffix[0] != '\t') {
		return "", false
	}
	return strings.TrimSpace(suffix), true
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
		if allOwnedProcessesExited(processes) {
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

func allOwnedProcessesExited(processes []OwnedProcess) bool {
	for _, process := range processes {
		if err := syscall.Kill(process.PID, syscall.Signal(0)); err == nil || !errors.Is(err, syscall.ESRCH) {
			return false
		}
	}
	return true
}
