package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRunParsesReadyJSONFixtureSizes(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{name: "empty result", count: 0},
		{name: "single bead", count: 1},
		{name: "large ready queue", count: 101},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(strings.NewReader(readyJSON(tt.count))); err != nil {
				t.Fatalf("run() error = %v", err)
			}
		})
	}
}

func readyJSON(count int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 1; i <= count; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"oro-ready-%03d","title":"Ready bead %03d","status":"open","priority":2,"parent_id":"oro-parent","type":"task","model":null,"tier":null,"worker_id":null,"context_percent":null,"last_heartbeat":null,"git_diff":null,"memory":null,"estimated_minutes":null,"acceptance_criteria":"Cmd: go test ./... | Assert: PASS","dependencies":null,"updated_at":null,"closed_at":null,"created_at":null,"description":null,"close_reason":null,"owner":null,"notes":null,"tags":null,"metadata":null,"labels":null}`, i, i)
	}
	b.WriteByte(']')
	return b.String()
}
