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
)

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
	out, err := exec.CommandContext(ctx, "ps", "axeww", "-o", "pid=,pgid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("list process environments: %w", err)
	}
	return ownedProcessesFromSnapshot(string(out), markers), nil
}

func ownedProcessesFromSnapshot(snapshot string, markers []string) []OwnedProcess {
	self := os.Getpid()
	var processes []OwnedProcess
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, pgidErr := strconv.Atoi(fields[1])
		if pidErr != nil || pgidErr != nil || pid <= 1 || pid == self ||
			!processenv.CommandContainsAllMarkers(strings.Join(fields[2:], " "), markers) {
			continue
		}
		processes = append(processes, OwnedProcess{PID: pid, PGID: pgid})
	}
	return processes
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
