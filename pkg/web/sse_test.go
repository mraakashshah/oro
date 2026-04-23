package web //nolint:testpackage // white-box: no public AddClient API yet, must access sseImpl internals

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatSSEEvent_ValidJSON(t *testing.T) {
	event := formatSSEEvent("bead_started", "oro-abc1", "worker-1")

	if !strings.Contains(event, "event: new-event\n") {
		t.Fatalf("event must contain named new-event frame, got %q", event)
	}
	if !strings.HasSuffix(event, "\n\n") {
		t.Fatalf("event must end with double newline, got %q", event)
	}

	parts := strings.Split(event, "\n\n")
	if len(parts) < 2 {
		t.Fatalf("event must contain at least one SSE frame, got %q", event)
	}
	frame := parts[0]
	lines := strings.Split(frame, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("first frame malformed: %q", frame)
	}
	jsonStr := strings.TrimPrefix(lines[1], "data: ")

	var parsed map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("event JSON is invalid: %v\nraw: %q", err, jsonStr)
	}

	if parsed["type"] != "bead_started" {
		t.Errorf("type = %q, want %q", parsed["type"], "bead_started")
	}
	if parsed["bead_id"] != "oro-abc1" {
		t.Errorf("bead_id = %q, want %q", parsed["bead_id"], "oro-abc1")
	}
	if parsed["worker_id"] != "worker-1" {
		t.Errorf("worker_id = %q, want %q", parsed["worker_id"], "worker-1")
	}
}

func TestFormatSSEEvent_SpecialChars(t *testing.T) {
	// Values with quotes and backslashes must produce valid JSON
	event := formatSSEEvent(`ev"ent`, `bead\1`, `worker"2`)

	parts := strings.Split(event, "\n\n")
	frame := strings.Split(parts[0], "\n")
	jsonStr := strings.TrimPrefix(frame[1], "data: ")

	var parsed map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("event JSON is invalid with special chars: %v\nraw: %q", err, jsonStr)
	}

	if parsed["type"] != `ev"ent` {
		t.Errorf("type = %q, want %q", parsed["type"], `ev"ent`)
	}
	if parsed["bead_id"] != `bead\1` {
		t.Errorf("bead_id = %q, want %q", parsed["bead_id"], `bead\1`)
	}
}

func TestSendBroadcastsToClients(t *testing.T) {
	b := NewSSEBroadcaster().(*sseImpl)

	ch := make(chan string, 1)
	b.mu.Lock()
	b.clients["test-client"] = ch
	b.mu.Unlock()

	b.Send("test_event", "oro-xyz", "worker-3")

	select {
	case msg := <-ch:
		if !strings.Contains(msg, "event: new-event") || !strings.Contains(msg, `"type":"test_event"`) {
			t.Errorf("unexpected event: %q", msg)
		}
	default:
		t.Error("expected a message on the client channel")
	}
}

func TestFormatSSEEvent_IncludesDashboardRefreshFrames(t *testing.T) {
	event := formatSSEEvent("merged", "oro-xyz", "worker-3")
	for _, want := range []string{
		"event: new-event",
		"event: parade-update",
		"event: worker-update",
		"event: throughput-update",
	} {
		if !strings.Contains(event, want) {
			t.Fatalf("event missing %q: %q", want, event)
		}
	}
}

func TestSendDropsSlowClient(t *testing.T) {
	b := NewSSEBroadcaster().(*sseImpl)

	// Unbuffered channel = always full
	ch := make(chan string)
	b.mu.Lock()
	b.clients["slow"] = ch
	b.mu.Unlock()

	// Should not block
	b.Send("test_event", "oro-xyz", "worker-3")
}
