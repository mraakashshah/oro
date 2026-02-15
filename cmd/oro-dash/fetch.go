package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"oro/pkg/protocol"
)

// fetchTimeout is how long to wait for bd CLI or dispatcher socket round-trip.
const fetchTimeout = 5 * time.Second

// parseBeadsOutput parses JSONL output (one JSON object per line) into a slice
// of protocol.Bead. Empty lines are skipped. Returns an error for malformed JSON.
func parseBeadsOutput(output string) ([]protocol.Bead, error) {
	var beads []protocol.Bead
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var b protocol.Bead
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			return nil, fmt.Errorf("parse bead JSON: %w", err)
		}
		beads = append(beads, b)
	}
	return beads, nil
}

// fetchBeads runs `bd list --format=jsonl` and parses the output into beads.
// Returns an empty slice on exec errors (bd not found, non-zero exit, etc).
//
//nolint:unused // Will be used when tick-based refresh integrates bead fetching
func fetchBeads(ctx context.Context) ([]protocol.Bead, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bd", "list", "--format=jsonl")
	out, err := cmd.Output()
	if err != nil {
		// bd not installed, not in PATH, or returned non-zero — not an error
		// condition for the dashboard; just means no bead data available.
		return nil, nil
	}

	return parseBeadsOutput(string(out))
}

// statusResponse mirrors the dispatcher's enriched status JSON structure.
// Defined locally to avoid coupling to cmd/oro's unexported types.
type statusResponse struct {
	State       string            `json:"state"`
	Workers     []workerEntry     `json:"workers"`
	WorkerCount int               `json:"worker_count"`
	Assignments map[string]string `json:"assignments"`
}

// workerEntry represents a single worker in the dispatcher status response.
type workerEntry struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// fetchWorkerStatus connects to the dispatcher UDS, sends a status directive,
// and extracts worker info from the response. Returns an empty slice if the
// socket doesn't exist or the connection fails — the dispatcher being offline
// is not an error condition.
func fetchWorkerStatus(ctx context.Context, socketPath string) ([]WorkerStatus, error) {
	// Fast path: if socket doesn't exist, dispatcher is offline.
	if _, err := os.Stat(socketPath); err != nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	// Connect to dispatcher UDS.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()

	// Send status directive: {"type":"DIRECTIVE","directive":{"op":"status"}}
	msg := protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op: "status",
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, nil
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, nil
	}

	// Read one line of JSON response (the ACK).
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, nil
	}

	var ack protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return nil, nil
	}

	if ack.Type != protocol.MsgACK || ack.ACK == nil || !ack.ACK.OK {
		return nil, nil
	}

	// Parse the status JSON from the ACK detail field.
	var resp statusResponse
	if err := json.Unmarshal([]byte(ack.ACK.Detail), &resp); err != nil {
		return nil, nil
	}

	// Convert to WorkerStatus slice.
	workers := make([]WorkerStatus, 0, len(resp.Workers))
	for _, w := range resp.Workers {
		workers = append(workers, WorkerStatus{
			ID:     w.ID,
			Status: w.State,
		})
	}

	return workers, nil
}
