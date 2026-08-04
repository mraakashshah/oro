package worker //nolint:testpackage // Evidence construction requires assignment-local worker state.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestWorkerHeartbeatAdvertisesReadyEvidenceCapability(t *testing.T) {
	t.Parallel()
	workerConn, dispatcherConn := net.Pipe()
	t.Cleanup(func() {
		_ = workerConn.Close()
		_ = dispatcherConn.Close()
	})
	w := NewWithConn("worker-ready-capability", workerConn, nil)

	sendErr := make(chan error, 1)
	go func() { sendErr <- w.SendHeartbeat(context.Background(), 7) }()
	var msg struct {
		Type      protocol.MessageType `json:"type"`
		Heartbeat struct {
			ProtocolVersion int      `json:"protocol_version"`
			Capabilities    []string `json:"capabilities"`
		} `json:"heartbeat"`
	}
	if err := json.NewDecoder(dispatcherConn).Decode(&msg); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}
	if msg.Heartbeat.ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d, want 1", msg.Heartbeat.ProtocolVersion)
	}
	if len(msg.Heartbeat.Capabilities) != 1 || msg.Heartbeat.Capabilities[0] != "ready-evidence-v1" {
		t.Fatalf("capabilities = %v, want [ready-evidence-v1]", msg.Heartbeat.Capabilities)
	}
}

func TestWorkerReconnectReportsAwaitingReviewAfterReadyWasSent(t *testing.T) {
	socketFile, err := os.CreateTemp("/tmp", "oro-ready-reconnect-*.sock")
	if err != nil {
		t.Fatalf("reserve reconnect socket path: %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("close socket reservation: %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("release socket reservation: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen for reconnect: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	workerConn, dispatcherConn := net.Pipe()
	t.Cleanup(func() {
		_ = workerConn.Close()
		_ = dispatcherConn.Close()
	})
	w := NewWithConn("worker-ready-reconnect", workerConn, nil)
	w.socketPath = socketPath
	w.reconnectInterval = time.Millisecond
	w.beadID = "oro-ready-reconnect"
	w.assignmentID = 23
	w.worktree = t.TempDir()
	w.qgEvidenceDir = t.TempDir()
	w.qgEvidencePath = filepath.Join(w.qgEvidenceDir, w.beadID, "23", qgEvidenceAttempt)
	w.targetSHA = "target-sha"
	w.targetBranch = "epic/oro-parent"

	reconnectErr := make(chan error, 1)
	go func() { reconnectErr <- w.reconnect(context.Background()) }()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept reconnect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var msg protocol.Message
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		t.Fatalf("decode reconnect: %v", err)
	}
	if err := <-reconnectErr; err != nil {
		t.Fatalf("worker reconnect: %v", err)
	}
	if msg.Reconnect == nil || msg.Reconnect.State != "awaiting_review" {
		t.Fatalf("reconnect payload = %#v, want awaiting_review", msg.Reconnect)
	}
	if msg.Reconnect.ProtocolVersion != protocol.WorkerProtocolVersion ||
		!msg.Reconnect.Supports(protocol.CapabilityReadyEvidenceV1) {
		t.Fatalf("reconnect protocol identity = version %d capabilities %v",
			msg.Reconnect.ProtocolVersion, msg.Reconnect.Capabilities)
	}
}

func TestWorkerQGEvidenceRejectsSymlinkedParents(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"bead", "assignment"} {
		t.Run(parent, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			external := t.TempDir()
			workerConn, dispatcherConn := net.Pipe()
			t.Cleanup(func() {
				_ = workerConn.Close()
				_ = dispatcherConn.Close()
			})
			w := NewWithConn("worker-ready-symlink", workerConn, nil)
			w.beadID = "oro-ready-symlink"
			w.worktree = t.TempDir()
			w.assignmentID = 19
			w.qgEvidenceDir = root
			w.targetSHA = "target-sha"

			assignmentDir := filepath.Join(root, w.beadID, strconv.FormatInt(w.assignmentID, 10))
			externalEvidence := filepath.Join(external, qgEvidenceAttempt)
			switch parent {
			case "bead":
				externalAssignment := filepath.Join(external, strconv.FormatInt(w.assignmentID, 10))
				if err := os.MkdirAll(externalAssignment, 0o700); err != nil {
					t.Fatalf("create external assignment: %v", err)
				}
				externalEvidence = filepath.Join(externalAssignment, qgEvidenceAttempt)
				if err := os.Symlink(external, filepath.Join(root, w.beadID)); err != nil {
					t.Fatalf("symlink bead parent: %v", err)
				}
			case "assignment":
				if err := os.Mkdir(filepath.Join(root, w.beadID), 0o700); err != nil {
					t.Fatalf("create bead parent: %v", err)
				}
				if err := os.Symlink(external, assignmentDir); err != nil {
					t.Fatalf("symlink assignment parent: %v", err)
				}
			}
			sentinel := []byte("external evidence must remain untouched")
			if err := os.WriteFile(externalEvidence, sentinel, 0o600); err != nil {
				t.Fatalf("write external sentinel: %v", err)
			}

			if err := w.writeQGEvidence(); err == nil {
				t.Fatal("symlinked evidence parent accepted")
			}
			got, err := os.ReadFile(externalEvidence)
			if err != nil {
				t.Fatalf("read external sentinel: %v", err)
			}
			if !bytes.Equal(got, sentinel) {
				t.Fatalf("external evidence changed to %q", got)
			}
		})
	}
}

func TestValidateAssignmentEvidenceIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid := protocol.AssignPayload{
		BeadID: "oro-ready", Worktree: t.TempDir(), AssignmentID: 7,
		QGEvidenceDir: root, TargetSHA: "target-sha",
	}
	if err := validateAssignmentEvidenceIdentity(&valid); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*protocol.AssignPayload)
	}{
		{name: "assignment", mutate: func(p *protocol.AssignPayload) { p.AssignmentID = 0 }},
		{name: "evidence root", mutate: func(p *protocol.AssignPayload) { p.QGEvidenceDir = "" }},
		{name: "absolute evidence root", mutate: func(p *protocol.AssignPayload) { p.QGEvidenceDir = "relative" }},
		{name: "target SHA", mutate: func(p *protocol.AssignPayload) { p.TargetSHA = "" }},
		{name: "safe bead", mutate: func(p *protocol.AssignPayload) { p.BeadID = "../escape" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := valid
			test.mutate(&payload)
			if err := validateAssignmentEvidenceIdentity(&payload); err == nil {
				t.Fatal("incomplete evidence identity accepted")
			}
		})
	}
}

func TestWorkerWritesCanonicalQGEvidenceAndSendsAssignedIdentity(t *testing.T) {
	t.Parallel()
	workerConn, dispatcherConn := net.Pipe()
	t.Cleanup(func() {
		_ = workerConn.Close()
		_ = dispatcherConn.Close()
	})
	root := t.TempDir()
	w := NewWithConn("worker-ready", workerConn, nil)
	w.beadID = "oro-ready"
	w.worktree = t.TempDir()
	w.assignmentID = 17
	w.qgEvidenceDir = root
	w.targetSHA = "target-sha"

	if err := w.writeQGEvidence(); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	wantPath := filepath.Join(root, "oro-ready", strconv.FormatInt(w.assignmentID, 10), qgEvidenceAttempt)
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat evidence: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("evidence mode = %o, want 600", info.Mode().Perm())
	}

	sendErr := make(chan error, 1)
	go func() { sendErr <- w.SendReadyForReview(context.Background()) }()
	var msg protocol.Message
	if err := json.NewDecoder(dispatcherConn).Decode(&msg); err != nil {
		t.Fatalf("decode READY: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("send READY: %v", err)
	}
	if msg.ReadyForReview == nil {
		t.Fatal("READY payload is nil")
	}
	want := protocol.ReadyForReviewPayload{
		BeadID: "oro-ready", WorkerID: "worker-ready", AssignmentID: 17,
		Worktree: w.worktree, QGEvidencePath: wantPath, TargetSHA: "target-sha",
	}
	if *msg.ReadyForReview != want {
		t.Fatalf("READY payload = %#v, want %#v", *msg.ReadyForReview, want)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	var evidence protocol.ReadyForReviewPayload
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence != want {
		t.Fatalf("evidence = %#v, want %#v", evidence, want)
	}
}
