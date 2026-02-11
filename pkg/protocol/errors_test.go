package protocol_test

import (
	"errors"
	"testing"

	"oro/pkg/protocol"
)

func TestQualityGateError_ErrorsAs(t *testing.T) {
	// Create a QualityGateError
	qgErr := &protocol.QualityGateError{
		BeadID:   "oro-test-1",
		WorkerID: "worker-1",
		Output:   "tests failed",
		Attempt:  2,
	}

	// Test that errors.As can extract the typed error directly
	var target *protocol.QualityGateError
	if !errors.As(qgErr, &target) {
		t.Fatal("errors.As failed to extract QualityGateError")
	}

	// Verify fields are preserved
	if target.BeadID != "oro-test-1" {
		t.Errorf("expected BeadID 'oro-test-1', got %q", target.BeadID)
	}
	if target.WorkerID != "worker-1" {
		t.Errorf("expected WorkerID 'worker-1', got %q", target.WorkerID)
	}
	if target.Output != "tests failed" {
		t.Errorf("expected Output 'tests failed', got %q", target.Output)
	}
	if target.Attempt != 2 {
		t.Errorf("expected Attempt 2, got %d", target.Attempt)
	}
}

func TestWorkerUnreachableError_ErrorsAs(t *testing.T) {
	// Create a WorkerUnreachableError
	wuErr := &protocol.WorkerUnreachableError{
		WorkerID: "worker-2",
		BeadID:   "oro-test-2",
		Reason:   "connection timeout",
	}

	// Test that errors.As can extract the typed error directly
	var target *protocol.WorkerUnreachableError
	if !errors.As(wuErr, &target) {
		t.Fatal("errors.As failed to extract WorkerUnreachableError")
	}

	// Verify fields are preserved
	if target.WorkerID != "worker-2" {
		t.Errorf("expected WorkerID 'worker-2', got %q", target.WorkerID)
	}
	if target.BeadID != "oro-test-2" {
		t.Errorf("expected BeadID 'oro-test-2', got %q", target.BeadID)
	}
	if target.Reason != "connection timeout" {
		t.Errorf("expected Reason 'connection timeout', got %q", target.Reason)
	}
}

func TestBeadNotFoundError_ErrorsAs(t *testing.T) {
	// Create a BeadNotFoundError
	bnfErr := &protocol.BeadNotFoundError{
		BeadID: "oro-missing",
	}

	// Test that errors.As can extract the typed error directly
	var target *protocol.BeadNotFoundError
	if !errors.As(bnfErr, &target) {
		t.Fatal("errors.As failed to extract BeadNotFoundError")
	}

	// Verify fields are preserved
	if target.BeadID != "oro-missing" {
		t.Errorf("expected BeadID 'oro-missing', got %q", target.BeadID)
	}
}

func TestQualityGateError_Error(t *testing.T) {
	qgErr := &protocol.QualityGateError{
		BeadID:   "oro-test-1",
		WorkerID: "worker-1",
		Output:   "tests failed",
		Attempt:  2,
	}

	errMsg := qgErr.Error()
	if errMsg == "" {
		t.Error("Error() returned empty string")
	}

	// Basic check that error message contains key info
	if !containsAll(errMsg, "quality gate", "oro-test-1", "worker-1") {
		t.Errorf("Error() message missing key info: %q", errMsg)
	}
}

func TestWorkerUnreachableError_Error(t *testing.T) {
	wuErr := &protocol.WorkerUnreachableError{
		WorkerID: "worker-2",
		BeadID:   "oro-test-2",
		Reason:   "connection timeout",
	}

	errMsg := wuErr.Error()
	if errMsg == "" {
		t.Error("Error() returned empty string")
	}

	// Basic check that error message contains key info
	if !containsAll(errMsg, "worker", "worker-2", "oro-test-2") {
		t.Errorf("Error() message missing key info: %q", errMsg)
	}
}

func TestBeadNotFoundError_Error(t *testing.T) {
	bnfErr := &protocol.BeadNotFoundError{
		BeadID: "oro-missing",
	}

	errMsg := bnfErr.Error()
	if errMsg == "" {
		t.Error("Error() returned empty string")
	}

	// Basic check that error message contains key info
	if !containsAll(errMsg, "bead", "oro-missing", "not found") {
		t.Errorf("Error() message missing key info: %q", errMsg)
	}
}

// containsAll checks if s contains all substrings
func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
