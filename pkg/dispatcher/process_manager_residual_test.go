//nolint:testpackage
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
	"testing"
	"time"

	"oro/pkg/processenv"
)

func TestInspectProcessEnvironmentsFindsExactOwnedProcess(t *testing.T) {
	if os.Getenv("ORO_TEST_OWNED_ENV_HELPER") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}

	socketPath := "/tmp/owned-process.sock"
	workerID := "owned-worker"
	cmd := exec.Command(os.Args[0], "-test.run=^TestInspectProcessEnvironmentsFindsExactOwnedProcess$") //nolint:gosec // test helper re-executes this binary
	cmd.Env = append(processenv.WithWorkerOwnership(os.Environ(), socketPath, workerID), "ORO_TEST_OWNED_ENV_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start owned process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned, err := inspectProcessEnvironments(ctx, []OwnedProcess{{PID: cmd.Process.Pid, PGID: cmd.Process.Pid}}, []string{
		"ORO_SOCKET_PATH=" + socketPath,
		"ORO_WORKER_ID=" + workerID,
	})
	if err != nil {
		t.Fatalf("inspect process environments: %v", err)
	}
	if len(owned) != 1 || owned[0].PID != cmd.Process.Pid {
		t.Fatalf("owned processes = %#v, want PID %d", owned, cmd.Process.Pid)
	}
}

func TestInspectProcessEnvironmentsRejectsIncompleteMarkers(t *testing.T) {
	for name, markers := range map[string][]string{
		"socket only":  {"ORO_SOCKET_PATH=/tmp/owned-process.sock"},
		"worker only":  {"ORO_WORKER_ID=owned-worker"},
		"empty socket": {"ORO_SOCKET_PATH=", "ORO_WORKER_ID=owned-worker"},
		"extra marker": {"ORO_SOCKET_PATH=/tmp/owned-process.sock", "ORO_WORKER_ID=owned-worker", "OTHER=value"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			owned, err := inspectProcessEnvironments(ctx, []OwnedProcess{{PID: strconv.IntSize, PGID: strconv.IntSize}}, markers)
			if err != nil {
				t.Fatalf("inspect process environments: %v", err)
			}
			if len(owned) != 0 {
				t.Fatalf("owned processes = %#v, want none for incomplete markers", owned)
			}
		})
	}
}

func TestInspectProcessEnvironmentsSkipsExitedProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned, err := inspectProcessEnvironments(ctx, []OwnedProcess{{PID: cmd.Process.Pid, PGID: cmd.Process.Pid}}, []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	})
	if err != nil {
		t.Fatalf("inspect process environments: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("owned processes = %#v, want none for exited process", owned)
	}
}

func TestInspectProcessEnvironmentsHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := inspectProcessEnvironments(ctx, []OwnedProcess{{PID: os.Getpid(), PGID: os.Getpid()}}, []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("inspect process environments error = %v, want context canceled", err)
	}
}

func TestScanOwnedProcessesHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := scanOwnedProcesses(ctx, []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan owned processes error = %v, want context canceled", err)
	}
}

func TestInspectProcessEnvironmentsBoundsLargeTableAndIgnoresArgvMarkers(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	processes := make([]OwnedProcess, residualScanBatchSize*3+17)
	for index := range processes {
		processes[index] = OwnedProcess{PID: 10_000 + index, PGID: 10_000 + index}
	}
	spoofedPID := processes[0].PID
	ownedPID := processes[len(processes)-1].PID
	batchCalls := 0
	maxBatchSize := 0
	reader := func(_ context.Context, batch []OwnedProcess) (processEnvironmentSnapshots, error) {
		batchCalls++
		maxBatchSize = max(maxBatchSize, len(batch))
		var commands, environments strings.Builder
		for _, process := range batch {
			command := "helper"
			environment := "PATH=/usr/bin"
			switch process.PID {
			case spoofedPID:
				command += " " + strings.Join(markers, " ")
			case ownedPID:
				environment += " " + strings.Join(markers, " ")
			}
			_, _ = fmt.Fprintf(&commands, "%d %s\n", process.PID, command)
			_, _ = fmt.Fprintf(&environments, "%d %d %s %s\n", process.PID, process.PGID, command, environment)
		}
		return processEnvironmentSnapshots{commands: commands.String(), environments: environments.String()}, nil
	}

	owned, err := inspectProcessEnvironmentsWithReader(context.Background(), processes, markers, reader)
	if err != nil {
		t.Fatalf("inspect process environments: %v", err)
	}
	if batchCalls != 4 || maxBatchSize > residualScanBatchSize {
		t.Fatalf("batch calls/max size = %d/%d, want 4/<=%d", batchCalls, maxBatchSize, residualScanBatchSize)
	}
	if len(owned) != 1 || owned[0].PID != ownedPID {
		t.Fatalf("owned processes = %#v, want only PID %d (argv spoof PID %d must not match)", owned, ownedPID, spoofedPID)
	}
}

func TestInspectProcessEnvironmentsReportsReaderFailureForLiveCandidate(t *testing.T) {
	readerErr := errors.New("ps failed")
	reader := func(context.Context, []OwnedProcess) (processEnvironmentSnapshots, error) {
		return processEnvironmentSnapshots{}, readerErr
	}
	_, err := inspectProcessEnvironmentsWithReader(context.Background(), []OwnedProcess{{PID: os.Getpid(), PGID: os.Getpid()}}, []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}, reader)
	if !errors.Is(err, readerErr) {
		t.Fatalf("inspect process environments error = %v, want reader failure", err)
	}
}

func TestInspectProcessEnvironmentsHonorsInFlightDeadline(t *testing.T) {
	reader := func(ctx context.Context, _ []OwnedProcess) (processEnvironmentSnapshots, error) {
		<-ctx.Done()
		return processEnvironmentSnapshots{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := inspectProcessEnvironmentsWithReader(ctx, []OwnedProcess{{PID: os.Getpid(), PGID: os.Getpid()}}, []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}, reader)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inspect process environments error = %v, want deadline exceeded", err)
	}
}
