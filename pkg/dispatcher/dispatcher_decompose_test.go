package dispatcher //nolint:testpackage // needs internal access to dispatcher state.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// exhaustQGRetries drives a worker through maxQGRetries quality-gate failures,
// reading intermediate re-ASSIGNs, so that the final failure triggers
// handleQGExhausted. Intermediate failures use unique per-attempt QGOutput to
// avoid stuck detection.
func exhaustQGRetries(t *testing.T, conn net.Conn, scanner *bufio.Scanner, workerID, beadID, finalQGOutput string) {
	t.Helper()
	for i := 1; i <= maxQGRetries; i++ {
		qgOut := finalQGOutput
		if i < maxQGRetries {
			qgOut = fmt.Sprintf("intermediate-fail-%d-for-%s", i, beadID)
		}
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          workerID,
				QualityGatePassed: false,
				QGOutput:          qgOut,
			},
		})
		if i < maxQGRetries {
			msg, ok := readMsgFromScanner(t, scanner, 10*time.Second)
			if !ok {
				t.Fatalf("exhaustQGRetries: expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("exhaustQGRetries: expected ASSIGN, got %s on attempt %d", msg.Type, i)
			}
		}
	}
}

func TestHandleQGExhaustedSkipsDecomposeForTriage(t *testing.T) {
	const beadID = "bead-decomp-skipped"
	const qgOutput = "unclassified qg exhausted output for triage"

	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	conn, scanner := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 2*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{
		ID:       beadID,
		Title:    "QG triage test",
		Priority: 1,
		Type:     "task",
		Model:    protocol.ModelOpus,
	}})

	readMsg(t, conn, 3*time.Second)
	exhaustQGRetries(t, conn, scanner, "w1", beadID, qgOutput)

	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
	}, 5*time.Second)

	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("unexpected decompose spawn count = %d, want 0", got)
	}
	beadSrc.mu.Lock()
	created := append([]createCall(nil), beadSrc.created...)
	deferCalls := append([]deferCall(nil), beadSrc.deferCalls...)
	beadSrc.mu.Unlock()
	for _, c := range created {
		if strings.Contains(c.title, "P0: QG exhausted") {
			t.Fatalf("unexpected legacy QG P0 bead: %+v", c)
		}
	}
	if len(deferCalls) == 0 || deferCalls[0].id != beadID {
		t.Fatalf("expected original bead to be deferred for triage, got %+v", deferCalls)
	}
	for _, m := range esc.Messages() {
		if strings.Contains(m, beadID) && strings.Contains(m, "quality gate failed") {
			t.Fatalf("unexpected manager escalation for triage-only QG exhaustion: %s", m)
		}
	}
}
