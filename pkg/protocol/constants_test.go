package protocol_test

import (
	"testing"

	"oro/pkg/protocol"
)

func TestEpicBranchPrefix(t *testing.T) {
	if protocol.EpicBranchPrefix != "epic/" {
		t.Errorf("EpicBranchPrefix = %q, want %q", protocol.EpicBranchPrefix, "epic/")
	}
}

func TestEpicBranch(t *testing.T) {
	// Test that epic branch prefix constant is defined and correct
	const expectedPrefix = "epic/"
	if protocol.EpicBranchPrefix != expectedPrefix {
		t.Fatalf("EpicBranchPrefix = %q, want %q", protocol.EpicBranchPrefix, expectedPrefix)
	}
}
