// Package memoryeval provides pure-Go evaluation helpers. CGO-dependent
// evaluation lives in harness.go.
package memoryeval

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const approvalMarker = "# APPROVED"

// HasApprovalMarker reports whether path contains a line equal to "# APPROVED".
// Returns false (not true) when the line is absent; only returns an error on I/O failure.
func HasApprovalMarker(path string) (bool, error) {
	f, err := os.Open(path) //nolint:gosec // eval corpus path is an explicit caller input
	if err != nil {
		return false, fmt.Errorf("open corpus: %w", err)
	}
	defer func() { _ = f.Close() }()

	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == approvalMarker {
			return true, nil
		}
	}
	if err := s.Err(); err != nil {
		return false, fmt.Errorf("scan corpus approval marker: %w", err)
	}
	return false, nil
}

// PrecisionAtK computes precision@k: the fraction of the top-k results that are
// relevant. Returns 0 when k ≤ 0, topKIDs is empty, or relevant is nil/empty.
func PrecisionAtK(topKIDs []int64, relevant map[int64]bool, k int) float64 {
	if k <= 0 || len(topKIDs) == 0 || len(relevant) == 0 {
		return 0
	}
	limit := min(k, len(topKIDs))
	hits := 0
	for _, id := range topKIDs[:limit] {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}
