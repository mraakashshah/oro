//nolint:testpackage
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		ready := os.NewFile(3, "owned-process-ready")
		if ready == nil {
			t.Fatal("open owned-process readiness pipe")
		}
		if _, err := ready.Write([]byte{1}); err != nil {
			t.Fatalf("signal owned-process readiness: %v", err)
		}
		_ = ready.Close()
		for {
			time.Sleep(time.Hour)
		}
	}

	socketPath := "/tmp/owned-process.sock"
	workerID := "owned-worker"
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create owned-process readiness pipe: %v", err)
	}
	t.Cleanup(func() { _ = readyReader.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestInspectProcessEnvironmentsFindsExactOwnedProcess$") //nolint:gosec // test helper re-executes this binary
	cmd.Env = append(processenv.WithWorkerOwnership(os.Environ(), socketPath, workerID), "ORO_TEST_OWNED_ENV_HELPER=1")
	cmd.ExtraFiles = []*os.File{readyWriter}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = readyWriter.Close()
		t.Fatalf("start owned process: %v", err)
	}
	_ = readyWriter.Close()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := readyReader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set owned-process readiness deadline: %v", err)
	}
	if _, err := io.ReadFull(readyReader, make([]byte, 1)); err != nil {
		t.Fatalf("wait for owned-process readiness: %v", err)
	}

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

func TestInspectProcessEnvironmentsPreservesEntryBoundaries(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	process := OwnedProcess{PID: 60_000, PGID: 60_000}
	reader := func(context.Context, []OwnedProcess) (processEnvironmentSnapshots, error) {
		return processEnvironmentSnapshots{
			environments: map[int][]string{process.PID: {
				"PATH=/usr/bin", "HOME=/tmp", markers[0], markers[1],
			}},
		}, nil
	}

	owned, err := inspectProcessEnvironmentsWithReader(context.Background(), []OwnedProcess{process}, markers, reader)
	if err != nil {
		t.Fatalf("inspect process environments: %v", err)
	}
	if len(owned) != 1 || owned[0] != process {
		t.Fatalf("owned processes = %#v, want %#v", owned, []OwnedProcess{process})
	}
}

func TestReadProcessEnvironmentEntriesPreservesDarwinBoundaries(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	raw := append([]byte{2, 0, 0, 0}, []byte("/bin/helper\x00helper\x00--flag\x00\x00PATH=/bin\x00NOTE=foreign "+markers[0]+" "+markers[1]+" text\x00"+markers[0]+"\x00ORDINARY=value with spaces\x00"+markers[1]+"\x00")...)
	entries, err := processenv.ParseDarwinEntries(raw)
	if err != nil {
		t.Fatalf("parse Darwin process environment entries: %v", err)
	}
	if !processenv.CommandContainsAllMarkers(entries, markers) {
		t.Fatalf("entries = %#v, want exact ownership markers", entries)
	}
	if processenv.CommandContainsAllMarkers(entries[:2], markers) {
		t.Fatal("a marker-shaped NOTE value must not prove ownership")
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
		environments := make(map[int][]string, len(batch))
		for _, process := range batch {
			environment := []string{"PATH=/usr/bin"}
			switch process.PID {
			case spoofedPID:
				environment = []string{"NOTE=" + strings.Join(markers, " ")}
			case ownedPID:
				environment = append(environment, markers...)
			}
			environments[process.PID] = environment
		}
		return processEnvironmentSnapshots{environments: environments}, nil
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

func TestOwnedProcessEnvironmentScanRejectsMarkersEmbeddedInSingleForeignVariable(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	const pid = 60_001

	owned := ownedProcessesFromEnvironmentSnapshot([]OwnedProcess{{PID: pid, PGID: pid}}, map[int][]string{
		pid: {"PATH=/usr/bin", "NOTE=foreign " + markers[0] + " " + markers[1] + " text"},
	}, markers)
	if len(owned) != 0 {
		t.Fatalf("owned processes = %#v, want none when both markers occur inside one foreign environment value", owned)
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

func TestReadProcessEnvironmentSnapshotsPropagatesLiveReadFailure(t *testing.T) {
	readErr := errors.New("read live process environment")
	processes := []OwnedProcess{{PID: os.Getpid(), PGID: os.Getpid()}}

	snapshots, err := readProcessEnvironmentSnapshotsWithReader(context.Background(), processes, func(int) ([]string, error) {
		return nil, readErr
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("readProcessEnvironmentSnapshots error = %v, want wrapped live-read failure", err)
	}
	if snapshots.environments != nil {
		t.Fatalf("readProcessEnvironmentSnapshots environments = %#v, want no false-success result", snapshots.environments)
	}
}

func TestReadProcessEnvironmentSnapshotsSkipsPermissionDenied(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	foreign := OwnedProcess{PID: os.Getpid(), PGID: os.Getpid()}
	owned := OwnedProcess{PID: 60_002, PGID: 60_002}
	processes := []OwnedProcess{foreign, owned}

	snapshots, err := readProcessEnvironmentSnapshotsWithReader(context.Background(), processes, func(pid int) ([]string, error) {
		if pid == foreign.PID {
			return nil, os.ErrPermission
		}
		return append([]string{"PATH=/usr/bin"}, markers...), nil
	})
	if err != nil {
		t.Fatalf("readProcessEnvironmentSnapshots error = %v, want permission-denied process skipped", err)
	}
	got := ownedProcessesFromEnvironmentSnapshot(processes, snapshots.environments, markers)
	if len(got) != 1 || got[0] != owned {
		t.Fatalf("owned processes = %#v, want %#v", got, []OwnedProcess{owned})
	}
}

func TestReadProcessEnvironmentSnapshotsSkipsDarwinVanishedPID(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/owned-process.sock",
		"ORO_WORKER_ID=owned-worker",
	}
	vanished := OwnedProcess{PID: 60_003, PGID: 60_003}
	owned := OwnedProcess{PID: 60_004, PGID: 60_004}
	processes := []OwnedProcess{vanished, owned}

	snapshots, err := readProcessEnvironmentSnapshotsWithReader(context.Background(), processes, func(pid int) ([]string, error) {
		if pid == vanished.PID {
			return nil, fmt.Errorf("kern.procargs2: %w", syscall.EINVAL)
		}
		return append([]string{"PATH=/usr/bin"}, markers...), nil
	})
	if err != nil {
		t.Fatalf("readProcessEnvironmentSnapshots error = %v, want vanished PID skipped", err)
	}
	got := ownedProcessesFromEnvironmentSnapshot(processes, snapshots.environments, markers)
	if len(got) != 1 || got[0] != owned {
		t.Fatalf("owned processes = %#v, want only %#v", got, []OwnedProcess{owned})
	}
}

func TestOwnedProcessExitedAfterPSRace(t *testing.T) {
	probes := 0
	exited := ownedProcessExitedWithProbe(context.Background(), 1234,
		func(int) error {
			probes++
			if probes == 1 {
				return nil
			}
			return syscall.ESRCH
		},
		func(context.Context, int) (string, error) {
			return "", errors.New("ps: process vanished")
		})
	if !exited {
		t.Fatal("ownedProcessExitedWithProbe = false, want exited after second ESRCH probe")
	}
	if probes != 2 {
		t.Fatalf("signal-zero probes = %d, want 2", probes)
	}
}
