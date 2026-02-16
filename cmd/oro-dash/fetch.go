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

// parseBeadsOutput parses JSON array output from `bd list --json` into a slice
// of protocol.Bead. Returns an error for malformed JSON.
func parseBeadsOutput(output string) ([]protocol.Bead, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}
	var beads []protocol.Bead
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		return nil, fmt.Errorf("parse beads JSON: %w", err)
	}
	return beads, nil
}

// fetchBeadsWithStatus fetches beads with a specific status filter.
func fetchBeadsWithStatus(ctx context.Context, status string) ([]protocol.Bead, error) {
	var cmd *exec.Cmd
	if status == "" {
		cmd = exec.CommandContext(ctx, "bd", "list", "--json")
	} else {
		cmd = exec.CommandContext(ctx, "bd", "list", "--status", status, "--json")
	}

	out, err := cmd.Output()
	if err != nil {
		// bd not installed, not in PATH, or returned non-zero
		return nil, fmt.Errorf("bd list: %w", err)
	}

	return parseBeadsOutput(string(out))
}

// fetchBeads fetches beads across all statuses (open, in_progress, blocked, closed).
// Returns an empty slice on exec errors (bd not found, non-zero exit, etc).
func fetchBeads(ctx context.Context) ([]protocol.Bead, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	// Fetch beads for each status
	statuses := []string{"open", "in_progress", "blocked", "closed"}
	var allBeads []protocol.Bead

	for _, status := range statuses {
		beads, err := fetchBeadsWithStatus(ctx, status)
		if err != nil {
			continue // Skip status on error
		}
		allBeads = append(allBeads, beads...)
	}

	return allBeads, nil
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
// and extracts worker info and assignments from the response. Returns empty slices if the
// socket doesn't exist or the connection fails — the dispatcher being offline
// is not an error condition.
func fetchWorkerStatus(ctx context.Context, socketPath string) ([]WorkerStatus, map[string]string, error) {
	// Fast path: if socket doesn't exist, dispatcher is offline.
	if _, err := os.Stat(socketPath); err != nil {
		return nil, nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	// Connect to dispatcher UDS.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, nil, nil
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
		return nil, nil, nil
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return nil, nil, nil
	}

	// Read one line of JSON response (the ACK).
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, nil, nil
	}

	var ack protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return nil, nil, nil
	}

	if ack.Type != protocol.MsgACK || ack.ACK == nil || !ack.ACK.OK {
		return nil, nil, nil
	}

	// Parse the status JSON from the ACK detail field.
	var resp statusResponse
	if err := json.Unmarshal([]byte(ack.ACK.Detail), &resp); err != nil {
		return nil, nil, nil
	}

	// Convert to WorkerStatus slice.
	workers := make([]WorkerStatus, 0, len(resp.Workers))
	for _, w := range resp.Workers {
		workers = append(workers, WorkerStatus{
			ID:     w.ID,
			Status: w.State,
		})
	}

	return workers, resp.Assignments, nil
}
