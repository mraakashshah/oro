package worker //nolint:testpackage // Evidence construction requires assignment-local worker state.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
			w.targetBranch = "main"
			w.targetSHA = strings.Repeat("1", 40)

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

			evidence, err := w.buildQGEvidence(qgEvidenceOptions{
				RunID:      "19:1",
				HeadSHA:    strings.Repeat("2", 40),
				ScriptHash: strings.Repeat("a", 64),
				StartedAt:  time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC),
				FinishedAt: time.Date(2026, time.August, 10, 3, 1, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("build evidence: %v", err)
			}
			if _, err := w.writeQGEvidence(evidence); err == nil {
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
	w.targetBranch = "main"
	w.targetSHA = strings.Repeat("1", 40)

	evidence, err := w.buildQGEvidence(qgEvidenceOptions{
		RunID:      "17:1",
		HeadSHA:    strings.Repeat("2", 40),
		ScriptHash: strings.Repeat("a", 64),
		StartedAt:  time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, time.August, 10, 3, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	ref, err := w.writeQGEvidence(evidence)
	if err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	wantPath := filepath.Join(root, "oro-ready", strconv.FormatInt(w.assignmentID, 10), qgEvidenceAttempt)
	if ref.Path != wantPath || ref.RunID != evidence.RunID {
		t.Fatalf("evidence ref = %#v, want path %q and run %q", ref, wantPath, evidence.RunID)
	}
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
		Worktree: w.worktree, QGEvidencePath: wantPath, TargetSHA: strings.Repeat("1", 40),
		ReadyAttempt: "1", QGEvidence: &evidence, QGEvidenceRef: &ref,
	}
	if !reflect.DeepEqual(*msg.ReadyForReview, want) {
		t.Fatalf("READY payload = %#v, want %#v", *msg.ReadyForReview, want)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	var stored protocol.QGEvidence
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if stored != evidence {
		t.Fatalf("evidence = %#v, want %#v", stored, evidence)
	}
}

func TestWorkerWritesDurableReadyEvidenceIdentity(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()
	script := []byte("#!/bin/sh\nprintf 'quality gate passed\\n'\n")
	scriptPath := filepath.Join(worktree, "quality_gate.sh")
	if err := os.WriteFile(scriptPath, script, 0o700); err != nil {
		t.Fatalf("write quality gate script: %v", err)
	}
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed test fixture commands
		cmd.Dir = worktree
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@oro.test")
	runGit("config", "user.name", "Oro Test")
	runGit("add", "quality_gate.sh")
	runGit("commit", "-m", "add quality gate")
	headSHA := runGit("rev-parse", "HEAD")

	workerConn, dispatcherConn := net.Pipe()
	t.Cleanup(func() {
		_ = workerConn.Close()
		_ = dispatcherConn.Close()
	})

	root := t.TempDir()
	w := NewWithConn("worker-ready", workerConn, nil)
	w.beadID = "oro-ready"
	w.worktree = worktree
	w.assignmentID = 17
	w.qgEvidenceDir = root
	w.targetBranch = "main"
	w.targetSHA = strings.Repeat("1", 40)
	output := []byte("quality gate passed\n")
	scriptHash := sha256.Sum256(script)
	outputHash := sha256.Sum256(output)

	done := make(chan struct{})
	go func() {
		w.runQGAndReport(context.Background())
		close(done)
	}()
	var msg protocol.Message
	decoder := json.NewDecoder(dispatcherConn)
	for {
		if err := decoder.Decode(&msg); err != nil {
			t.Fatalf("decode QG result: %v", err)
		}
		if msg.Type != protocol.MsgStatus {
			break
		}
	}
	<-done
	if msg.Type != protocol.MsgReadyForReview || msg.ReadyForReview == nil ||
		msg.ReadyForReview.QGEvidence == nil || msg.ReadyForReview.QGEvidenceRef == nil {
		t.Fatalf("production QG did not emit durable READY: %#v", msg)
	}
	evidence := *msg.ReadyForReview.QGEvidence
	ref := *msg.ReadyForReview.QGEvidenceRef
	if evidence.RunID != "17:1" || evidence.AssignmentID != 17 || evidence.BeadID != w.beadID ||
		evidence.WorkerID != w.ID || evidence.HeadSHA != headSHA || evidence.TargetBranch != "main" ||
		evidence.TargetSHA != strings.Repeat("1", 40) || evidence.ScriptHash != hex.EncodeToString(scriptHash[:]) ||
		evidence.OutputHash != hex.EncodeToString(outputHash[:]) || evidence.Mode != "worker" || !evidence.Passed {
		t.Fatalf("production evidence identity = %#v", evidence)
	}
	startedAt, err := time.Parse(time.RFC3339, evidence.StartedAt)
	if err != nil {
		t.Fatalf("parse evidence start: %v", err)
	}
	finishedAt, err := time.Parse(time.RFC3339, evidence.FinishedAt)
	if err != nil || !finishedAt.After(startedAt) {
		t.Fatalf("evidence timing = %q..%q: %v", evidence.StartedAt, evidence.FinishedAt, err)
	}
	wantPath := filepath.Join(root, "oro-ready", "17", qgEvidenceAttempt)
	if ref.RunID != evidence.RunID || ref.Path != wantPath {
		t.Fatalf("evidence ref = %#v, want run/path %q/%q", ref, evidence.RunID, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	fileHash := sha256.Sum256(data)
	if ref.SHA256 != hex.EncodeToString(fileHash[:]) {
		t.Fatalf("evidence ref hash = %q, want %q", ref.SHA256, hex.EncodeToString(fileHash[:]))
	}
	var stored protocol.QGEvidence
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if stored != evidence {
		t.Fatalf("stored evidence = %#v, want %#v", stored, evidence)
	}
}

func TestWorkerBuildsOrderedSubsecondEvidenceTiming(t *testing.T) {
	startedAt := time.Date(2026, time.August, 10, 3, 0, 0, 100, time.UTC)
	w := NewWithConn("worker-ready-timing", nil, nil)
	w.beadID = "oro-ready-timing"
	w.assignmentID = 29
	w.qgEvidenceDir = t.TempDir()
	w.targetBranch = "main"
	w.targetSHA = strings.Repeat("1", 40)

	evidence, err := w.buildQGEvidence(qgEvidenceOptions{
		RunID:      "29:1",
		HeadSHA:    strings.Repeat("2", 40),
		ScriptHash: strings.Repeat("a", 64),
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(100 * time.Nanosecond),
	})
	if err != nil {
		t.Fatalf("build subsecond evidence: %v", err)
	}
	if evidence.StartedAt == evidence.FinishedAt {
		t.Fatalf("subsecond evidence timing collapsed to %q", evidence.StartedAt)
	}
	if evidence.OutputHash != sha256Hex(nil) {
		t.Fatalf("empty output hash = %q, want %q", evidence.OutputHash, sha256Hex(nil))
	}
	parsedStart, err := time.Parse(time.RFC3339, evidence.StartedAt)
	if err != nil {
		t.Fatalf("parse evidence start: %v", err)
	}
	parsedFinish, err := time.Parse(time.RFC3339, evidence.FinishedAt)
	if err != nil {
		t.Fatalf("parse evidence finish: %v", err)
	}
	if !parsedStart.Equal(startedAt) || !parsedFinish.Equal(startedAt.Add(100*time.Nanosecond)) ||
		!parsedFinish.After(parsedStart) {
		t.Fatalf("subsecond evidence timing = %s..%s", parsedStart, parsedFinish)
	}
}

func TestWorkerResetDoesNotLeakPriorReadyEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workerConn, dispatcherConn := net.Pipe()
	t.Cleanup(func() {
		_ = workerConn.Close()
		_ = dispatcherConn.Close()
	})
	w := NewWithConn("worker-ready-reset", workerConn, nil)
	w.qgEvidencePath = "/previous/evidence.json"
	w.qgEvidence = &protocol.QGEvidence{RunID: "previous:1"}
	w.qgEvidenceRef = &protocol.QGEvidenceRef{RunID: "previous:1"}

	w.resetForNewAssignment(&protocol.AssignPayload{
		BeadID:        "oro-next",
		AssignmentID:  18,
		QGEvidenceDir: t.TempDir(),
		TargetSHA:     strings.Repeat("3", 40),
	}, WorkerExecutionContext{})
	t.Cleanup(w.closeLogFile)

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
		t.Fatal("READY payload missing")
	}
	if msg.ReadyForReview.QGEvidence != nil || msg.ReadyForReview.QGEvidenceRef != nil {
		t.Fatalf("READY leaked prior evidence: %#v", msg.ReadyForReview)
	}
}

func TestWorkerReadyEvidenceMutationOwners(t *testing.T) {
	runReadyEvidenceMutationOwnerCases(t)
}

func runReadyEvidenceMutationOwnerCases(t *testing.T) {
	t.Helper()
	const targetSHA = "0123456789012345678901234567890123456789"

	t.Run("buildQGEvidence", func(t *testing.T) {
		w := NewWithConn("owner-build", nil, nil)
		w.beadID = "owner-bead"
		w.worktree = t.TempDir()
		w.assignmentID = 31
		w.qgEvidenceDir = t.TempDir()
		w.targetBranch = "main"
		w.targetSHA = targetSHA

		output := []byte("quality gate passed\n")
		startedAt := time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC)
		finishedAt := time.Date(2026, time.August, 10, 3, 1, 0, 0, time.UTC)
		evidence, err := w.buildQGEvidence(qgEvidenceOptions{
			RunID:      "31:1",
			HeadSHA:    targetSHA,
			ScriptHash: strings.Repeat("a", 64),
			Output:     output,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
		})
		if err != nil {
			t.Fatalf("build evidence: %v", err)
		}
		if evidence.RunID != "31:1" || evidence.AssignmentID != 31 ||
			evidence.BeadID != w.beadID || evidence.WorkerID != w.ID ||
			evidence.HeadSHA != targetSHA || evidence.TargetBranch != "main" || evidence.TargetSHA != targetSHA ||
			evidence.ScriptHash != strings.Repeat("a", 64) ||
			evidence.OutputHash != sha256Hex(output) || evidence.Mode != "worker" ||
			evidence.StartedAt != startedAt.Format(time.RFC3339Nano) ||
			evidence.FinishedAt != finishedAt.Format(time.RFC3339Nano) || !evidence.Passed {
			t.Fatalf("evidence identity = %#v", evidence)
		}
	})

	t.Run("writeQGEvidence", func(t *testing.T) {
		w := NewWithConn("owner-write", nil, nil)
		w.beadID = "owner-bead"
		w.worktree = t.TempDir()
		w.assignmentID = 32
		w.qgEvidenceDir = t.TempDir()
		w.targetBranch = "main"
		w.targetSHA = targetSHA
		startedAt := time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC)
		finishedAt := time.Date(2026, time.August, 10, 3, 1, 0, 0, time.UTC)
		evidence, err := w.buildQGEvidence(qgEvidenceOptions{
			RunID:      "32:1",
			HeadSHA:    targetSHA,
			ScriptHash: strings.Repeat("b", 64),
			Output:     []byte("passed\n"),
			StartedAt:  time.Date(2026, time.August, 10, 3, 0, 0, 0, time.UTC),
			FinishedAt: time.Date(2026, time.August, 10, 3, 1, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("build evidence: %v", err)
		}
		ref, err := w.writeQGEvidence(evidence)
		if err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		wantPath := filepath.Join(w.qgEvidenceDir, w.beadID, strconv.FormatInt(w.assignmentID, 10), qgEvidenceAttempt)
		if ref.RunID != evidence.RunID || ref.Path != wantPath || ref.SHA256 == "" {
			t.Fatalf("evidence ref = %#v", ref)
		}
		data, err := os.ReadFile(ref.Path)
		if err != nil {
			t.Fatalf("read evidence file: %v", err)
		}
		var stored protocol.QGEvidence
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatalf("decode stored evidence: %v", err)
		}
		if stored != evidence {
			t.Fatalf("stored evidence = %#v, want %#v", stored, evidence)
		}
		fileHash := sha256.Sum256(data)
		if ref.SHA256 != hex.EncodeToString(fileHash[:]) {
			t.Fatalf("evidence ref hash = %q, want %q", ref.SHA256, hex.EncodeToString(fileHash[:]))
		}
		info, err := os.Stat(ref.Path)
		if err != nil {
			t.Fatalf("stat evidence: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("evidence mode = %o, want 600", info.Mode().Perm())
		}

		t.Run("symlinked parent fails closed", func(t *testing.T) {
			root := t.TempDir()
			external := t.TempDir()
			w := NewWithConn("owner-write-symlink", nil, nil)
			w.beadID = "owner-bead"
			w.worktree = t.TempDir()
			w.assignmentID = 36
			w.qgEvidenceDir = root
			w.targetBranch = "main"
			w.targetSHA = targetSHA
			if err := os.Symlink(external, filepath.Join(root, w.beadID)); err != nil {
				t.Fatalf("symlink evidence parent: %v", err)
			}
			evidence, err := w.buildQGEvidence(qgEvidenceOptions{
				RunID: "36:1", HeadSHA: targetSHA, ScriptHash: strings.Repeat("d", 64),
				StartedAt: startedAt, FinishedAt: finishedAt,
			})
			if err != nil {
				t.Fatalf("build evidence: %v", err)
			}
			if _, err := w.writeQGEvidence(evidence); err == nil {
				t.Fatal("symlinked evidence parent accepted")
			}
		})

		t.Run("non-directory root fails closed", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence-root")
			if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("write root sentinel: %v", err)
			}
			w := NewWithConn("owner-write-file", nil, nil)
			w.beadID = "owner-bead"
			w.worktree = t.TempDir()
			w.assignmentID = 37
			w.qgEvidenceDir = root
			w.targetBranch = "main"
			w.targetSHA = targetSHA
			evidence, err := w.buildQGEvidence(qgEvidenceOptions{
				RunID: "37:1", HeadSHA: targetSHA, ScriptHash: strings.Repeat("e", 64),
				StartedAt: startedAt, FinishedAt: finishedAt,
			})
			if err != nil {
				t.Fatalf("build evidence: %v", err)
			}
			if _, err := w.writeQGEvidence(evidence); err == nil {
				t.Fatal("non-directory evidence root accepted")
			}
		})
	})

	t.Run("SendReadyForReview", func(t *testing.T) {
		workerConn, dispatcherConn := net.Pipe()
		t.Cleanup(func() {
			_ = workerConn.Close()
			_ = dispatcherConn.Close()
		})
		w := NewWithConn("owner-ready", workerConn, nil)
		w.beadID = "owner-bead"
		w.worktree = t.TempDir()
		w.assignmentID = 33
		w.targetSHA = targetSHA
		w.qgEvidencePath = filepath.Join(t.TempDir(), "evidence.json")
		evidence := protocol.QGEvidence{RunID: "33:1", AssignmentID: 33, BeadID: w.beadID, WorkerID: w.ID,
			HeadSHA: targetSHA, TargetBranch: "main", TargetSHA: targetSHA,
			ScriptHash: strings.Repeat("c", 64), OutputHash: sha256Hex([]byte("passed\n")),
			Mode: "worker", Passed: true, StartedAt: "2026-08-10T03:00:00Z", FinishedAt: "2026-08-10T03:01:00Z"}
		ref := protocol.QGEvidenceRef{RunID: evidence.RunID, Path: w.qgEvidencePath, SHA256: strings.Repeat("d", 64)}
		w.qgEvidence = &evidence
		w.qgEvidenceRef = &ref
		sendErr := make(chan error, 1)
		go func() { sendErr <- w.SendReadyForReview(context.Background()) }()
		msg, err := readReadyEvidenceMessage(t, dispatcherConn)
		if err != nil {
			_ = workerConn.Close()
			_ = dispatcherConn.Close()
			t.Fatalf("read READY: %v", err)
		}
		select {
		case err := <-sendErr:
			if err != nil {
				t.Fatalf("send READY: %v", err)
			}
		case <-time.After(time.Second):
			_ = workerConn.Close()
			_ = dispatcherConn.Close()
			t.Fatal("SendReadyForReview did not complete")
		}
		if msg.Type != protocol.MsgReadyForReview || msg.ReadyForReview == nil ||
			msg.ReadyForReview.QGEvidence == nil || msg.ReadyForReview.QGEvidenceRef == nil {
			t.Fatalf("READY payload = %#v", msg)
		}
		if msg.ReadyForReview.AssignmentID != 33 || msg.ReadyForReview.ReadyAttempt != "1" ||
			msg.ReadyForReview.BeadID != w.beadID || msg.ReadyForReview.WorkerID != w.ID ||
			msg.ReadyForReview.Worktree != w.worktree || msg.ReadyForReview.TargetSHA != targetSHA ||
			msg.ReadyForReview.QGEvidencePath != w.qgEvidencePath ||
			!reflect.DeepEqual(*msg.ReadyForReview.QGEvidence, evidence) ||
			!reflect.DeepEqual(*msg.ReadyForReview.QGEvidenceRef, ref) {
			t.Fatalf("READY identity = %#v", msg.ReadyForReview)
		}
	})

	t.Run("readReadyEvidenceMessage rejects malformed input", func(t *testing.T) {
		workerConn, dispatcherConn := net.Pipe()
		t.Cleanup(func() {
			_ = workerConn.Close()
			_ = dispatcherConn.Close()
		})
		writerDone := make(chan struct{})
		go func() {
			_, _ = workerConn.Write([]byte("not-json\n"))
			_ = workerConn.Close()
			close(writerDone)
		}()
		if _, err := readReadyEvidenceMessage(t, dispatcherConn); err == nil {
			t.Fatal("malformed READY message accepted")
		}
		select {
		case <-writerDone:
		case <-time.After(time.Second):
			t.Fatal("malformed-message writer did not finish")
		}
	})

	t.Run("readReadyEvidenceMessage rejects EOF", func(t *testing.T) {
		workerConn, dispatcherConn := net.Pipe()
		t.Cleanup(func() {
			_ = workerConn.Close()
			_ = dispatcherConn.Close()
		})
		_ = workerConn.Close()
		if _, err := readReadyEvidenceMessage(t, dispatcherConn); err == nil {
			t.Fatal("EOF accepted as READY message")
		}
	})

	t.Run("resetForNewAssignment", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		w := NewWithConn("owner-reset", nil, nil)
		w.beadID = "old-bead"
		w.assignmentID = 34
		w.qgEvidencePath = "/previous/evidence.json"
		w.qgEvidence = &protocol.QGEvidence{RunID: "34:1"}
		w.qgEvidenceRef = &protocol.QGEvidenceRef{RunID: "34:1"}
		w.resetForNewAssignment(&protocol.AssignPayload{
			BeadID:        "new-bead",
			AssignmentID:  35,
			QGEvidenceDir: t.TempDir(),
			TargetBranch:  "main",
			TargetSHA:     targetSHA,
		}, WorkerExecutionContext{})
		t.Cleanup(w.closeLogFile)
		if w.beadID != "new-bead" || w.assignmentID != 35 || w.qgEvidencePath != "" ||
			w.qgEvidence != nil || w.qgEvidenceRef != nil {
			t.Fatalf("reset state leaked prior evidence: bead=%q assignment=%d path=%q evidence=%#v ref=%#v",
				w.beadID, w.assignmentID, w.qgEvidencePath, w.qgEvidence, w.qgEvidenceRef)
		}
	})
}

func readReadyEvidenceMessage(t *testing.T, conn net.Conn) (protocol.Message, error) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return protocol.Message{}, err
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return protocol.Message{}, err
		}
		return protocol.Message{}, io.EOF
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		return protocol.Message{}, err
	}
	return msg, nil
}
