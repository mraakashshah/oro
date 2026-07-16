package processenv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

const (
	// SocketPathEnv scopes an Oro subprocess to one dispatcher/project socket.
	SocketPathEnv = "ORO_SOCKET_PATH"
	// WorkerIDEnv identifies the managed worker that owns a subprocess tree.
	WorkerIDEnv = "ORO_WORKER_ID"
)

// WithWorkerOwnership replaces inherited ownership values with the exact
// dispatcher socket and worker ID for a managed worker subprocess.
//
//oro:testonly — production wiring is provided by the referenced oro-533v foundation.
func WithWorkerOwnership(env []string, socketPath, workerID string) []string {
	markers := WorkerOwnershipMarkers(socketPath, workerID)
	out := make([]string, 0, len(env)+len(markers))
	out = append(out, markers...)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == SocketPathEnv || key == WorkerIDEnv) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// WorkerOwnershipMarkers returns the complete marker tuple required to own a
// worker subprocess. An incomplete scope intentionally produces no markers.
func WorkerOwnershipMarkers(socketPath, workerID string) []string {
	if socketPath == "" || workerID == "" {
		return nil
	}
	return []string{SocketPathEnv + "=" + socketPath, WorkerIDEnv + "=" + workerID}
}

// CommandContainsAllMarkers reports whether entries contain every exact
// ownership marker. Callers must preserve entry boundaries so marker-shaped
// text within another variable's value never proves ownership.
//
//oro:testonly — production wiring is provided by the referenced oro-533v foundation.
func CommandContainsAllMarkers(entries, markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	found := make(map[string]bool, len(entries))
	for _, entry := range entries {
		found[entry] = true
	}
	for _, marker := range markers {
		if marker == "" || !found[marker] {
			return false
		}
	}
	return true
}

func splitNULDelimitedEntries(contents []byte) []string {
	entries := make([]string, 0)
	for len(contents) > 0 {
		entry, rest, found := bytes.Cut(contents, []byte{'\x00'})
		if !found {
			entries = append(entries, string(entry))
			break
		}
		if len(entry) > 0 {
			entries = append(entries, string(entry))
		}
		contents = rest
	}
	return entries
}

// ParseDarwinEntries decodes the NUL-delimited environment suffix of a
// kern.procargs2 payload without treating whitespace as an entry separator.
func ParseDarwinEntries(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("kern.procargs2 payload too short")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest, ok := skipNULDelimitedEntry(raw[4:])
	if !ok {
		return nil, fmt.Errorf("kern.procargs2 executable path missing")
	}
	rest = bytes.TrimLeft(rest, "\x00")
	for range argc {
		rest, ok = skipNULDelimitedEntry(rest)
		if !ok {
			return nil, fmt.Errorf("kern.procargs2 argv truncated")
		}
	}
	return splitNULDelimitedEntries(bytes.TrimLeft(rest, "\x00")), nil
}

func skipNULDelimitedEntry(contents []byte) (remaining []byte, ok bool) {
	_, remaining, ok = bytes.Cut(contents, []byte{'\x00'})
	return remaining, ok
}
