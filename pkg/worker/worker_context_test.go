package worker_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestWorkerContextIsAssignmentLocal(t *testing.T) {
	t.Setenv("ORO_WORKER_BEAD_ID", "caller-identity")

	socketFile, err := os.CreateTemp("/tmp", "oro-worker-context-*.sock")
	if err != nil {
		t.Fatalf("create socket path: %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("close socket path: %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("remove socket placeholder: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	spawner := &executionContextSpawner{}
	connCh := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			connCh <- conn
		}
	}()

	w, err := worker.NewWithRuntimeSpawner("w-context", socketPath, worker.NewRuntimeSpawnerRouter(spawner, spawner))
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	var dispatcherConn net.Conn
	select {
	case dispatcherConn = <-connCh:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not connect")
	}
	t.Cleanup(func() { _ = dispatcherConn.Close() })

	_ = readMessage(t, dispatcherConn) // initial heartbeat
	worktree := validAssignWorktree(t, "context")
	capabilityFile := filepath.Join(worktree, protocol.OroDir, "assignment-capability.json")
	for _, assign := range []*protocol.AssignPayload{
		{
			BeadID: "bead-one", Worktree: worktree, AssignmentID: 101, Generation: 1,
			ActorRole: "execution_worker", Capability: "/tmp/capability-one",
		},
		{
			BeadID: "bead-two", Worktree: worktree, AssignmentID: 202, Generation: 2,
			ActorRole: "recovery_worker", Capability: "/tmp/capability-two",
		},
	} {
		sendMessage(t, dispatcherConn, protocol.Message{Type: protocol.MsgAssign, Assign: assign})
		if msg := readMessage(t, dispatcherConn); msg.Type != protocol.MsgStatus {
			t.Fatalf("ASSIGN %d response = %s, want STATUS", assign.AssignmentID, msg.Type)
		}
	}

	contexts := spawner.Contexts()
	if len(contexts) != 2 {
		t.Fatalf("spawn contexts = %d, want 2", len(contexts))
	}
	for i, want := range []worker.WorkerExecutionContext{
		{AssignmentID: 101, Generation: 1, Role: "execution_worker", SocketPath: socketPath, CapabilityFile: capabilityFile},
		{AssignmentID: 202, Generation: 2, Role: "recovery_worker", SocketPath: socketPath, CapabilityFile: capabilityFile},
	} {
		if contexts[i] != want {
			t.Fatalf("spawn context %d = %#v, want %#v", i, contexts[i], want)
		}
	}
	if got := os.Getenv("ORO_WORKER_BEAD_ID"); got != "caller-identity" {
		t.Fatalf("process-global worker identity leaked: got %q", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("worker run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	if _, err := os.Stat(capabilityFile); !os.IsNotExist(err) {
		t.Fatalf("capability file after worker termination: err = %v, want not exist", err)
	}
}

type executionContextSpawner struct {
	mu       sync.Mutex
	contexts []worker.WorkerExecutionContext
}

func (s *executionContextSpawner) Spawn(ctx context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	execution, ok := worker.ExecutionContextFrom(ctx)
	if !ok {
		return nil, nil, nil, context.Canceled
	}
	s.mu.Lock()
	s.contexts = append(s.contexts, execution)
	s.mu.Unlock()
	return newMockProcess(), nil, nil, nil
}

func (s *executionContextSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatLineText
}

func (s *executionContextSpawner) Contexts() []worker.WorkerExecutionContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	contexts := make([]worker.WorkerExecutionContext, len(s.contexts))
	copy(contexts, s.contexts)
	return contexts
}
