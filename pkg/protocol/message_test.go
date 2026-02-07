package protocol_test

import (
	"encoding/json"
	"testing"

	"oro/pkg/protocol"
)

func TestMessageTypes(t *testing.T) {
	t.Parallel()

	// All expected message type constants must be defined.
	types := []protocol.MessageType{
		protocol.MsgAssign,
		protocol.MsgShutdown,
		protocol.MsgHeartbeat,
		protocol.MsgStatus,
		protocol.MsgHandoff,
		protocol.MsgDone,
		protocol.MsgReadyForReview,
		protocol.MsgReconnect,
	}

	expected := []string{
		"ASSIGN",
		"SHUTDOWN",
		"HEARTBEAT",
		"STATUS",
		"HANDOFF",
		"DONE",
		"READY_FOR_REVIEW",
		"RECONNECT",
	}

	for i, mt := range types {
		if string(mt) != expected[i] {
			t.Errorf("expected %q, got %q", expected[i], mt)
		}
	}
}

func TestMessageJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  protocol.Message
	}{
		{
			name: "ASSIGN",
			msg: protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:   "bead-123",
					Worktree: "/tmp/worktree",
				},
			},
		},
		{
			name: "SHUTDOWN",
			msg: protocol.Message{
				Type: protocol.MsgShutdown,
			},
		},
		{
			name: "HEARTBEAT",
			msg: protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					BeadID:     "bead-456",
					WorkerID:   "worker-1",
					ContextPct: 42,
				},
			},
		},
		{
			name: "STATUS",
			msg: protocol.Message{
				Type: protocol.MsgStatus,
				Status: &protocol.StatusPayload{
					BeadID:   "bead-789",
					WorkerID: "worker-2",
					State:    "running",
					Result:   "ok",
				},
			},
		},
		{
			name: "HANDOFF",
			msg: protocol.Message{
				Type: protocol.MsgHandoff,
				Handoff: &protocol.HandoffPayload{
					BeadID:   "bead-abc",
					WorkerID: "worker-3",
				},
			},
		},
		{
			name: "DONE",
			msg: protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:   "bead-def",
					WorkerID: "worker-4",
				},
			},
		},
		{
			name: "READY_FOR_REVIEW",
			msg: protocol.Message{
				Type: protocol.MsgReadyForReview,
				ReadyForReview: &protocol.ReadyForReviewPayload{
					BeadID:   "bead-ghi",
					WorkerID: "worker-5",
				},
			},
		},
		{
			name: "RECONNECT",
			msg: protocol.Message{
				Type: protocol.MsgReconnect,
				Reconnect: &protocol.ReconnectPayload{
					WorkerID:   "worker-6",
					BeadID:     "bead-jkl",
					State:      "paused",
					ContextPct: 75,
					BufferedEvents: []protocol.Message{
						{
							Type: protocol.MsgHeartbeat,
							Heartbeat: &protocol.HeartbeatPayload{
								BeadID:     "bead-jkl",
								WorkerID:   "worker-6",
								ContextPct: 70,
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}

			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}

			// Re-marshal both and compare JSON to verify round-trip equality.
			wantJSON, _ := json.Marshal(tc.msg)
			gotJSON, _ := json.Marshal(got)

			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch for %s:\n  want: %s\n  got:  %s", tc.name, wantJSON, gotJSON)
			}
		})
	}
}
