package protocol_test

import (
	"encoding/json"
	"testing"

	"oro/pkg/protocol"
)

func TestRerankByIDsRequestRoundTrip(t *testing.T) {
	req := protocol.RerankByIDsRequest{
		Query:     "what retries a failed bead",
		MemoryIDs: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var unmarshaled protocol.RerankByIDsRequest
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if unmarshaled.Query != req.Query {
		t.Errorf("Query mismatch: got %q, want %q", unmarshaled.Query, req.Query)
	}
	if len(unmarshaled.MemoryIDs) != len(req.MemoryIDs) {
		t.Errorf("MemoryIDs length mismatch: got %d, want %d", len(unmarshaled.MemoryIDs), len(req.MemoryIDs))
	}
	for i, id := range unmarshaled.MemoryIDs {
		if id != req.MemoryIDs[i] {
			t.Errorf("MemoryIDs[%d] mismatch: got %d, want %d", i, id, req.MemoryIDs[i])
		}
	}
}

func TestRerankByIDsRequestMarshalSizeUnder16KB(t *testing.T) {
	// 50 IDs + 256-char query
	query := "what retries a failed bead with comprehensive error handling and exponential backoff strategy for distributed systems"
	for len(query) < 256 {
		query += " additional content"
	}
	query = query[:256]

	ids := make([]int64, 50)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	req := protocol.RerankByIDsRequest{
		Query:     query,
		MemoryIDs: ids,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	const maxSize = 16384 // 16 KB
	if len(data) >= maxSize {
		t.Errorf("Marshaled size %d bytes exceeds limit of %d bytes", len(data), maxSize)
	}
}

func TestRerankByIDsResponseRoundTrip(t *testing.T) {
	resp := protocol.RerankByIDsResponse{
		Scores: []float64{0.9, 0.8, 0.7, 0.6, 0.5},
		Err:    "",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var unmarshaled protocol.RerankByIDsResponse
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(unmarshaled.Scores) != len(resp.Scores) {
		t.Errorf("Scores length mismatch: got %d, want %d", len(unmarshaled.Scores), len(resp.Scores))
	}
	for i, score := range unmarshaled.Scores {
		if score != resp.Scores[i] {
			t.Errorf("Scores[%d] mismatch: got %f, want %f", i, score, resp.Scores[i])
		}
	}
	if unmarshaled.Err != resp.Err {
		t.Errorf("Err mismatch: got %q, want %q", unmarshaled.Err, resp.Err)
	}
}
