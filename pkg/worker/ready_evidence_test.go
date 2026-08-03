package worker //nolint:testpackage // Evidence construction requires assignment-local worker state.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"oro/pkg/protocol"
)

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
