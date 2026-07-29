package reviewcontract_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/reviewcontract"
)

func TestReviewRecoveryFindingWireContractRoundTrip(t *testing.T) {
	want := &protocol.ReviewRecovery{
		CheckpointID:    44,
		RejectedHeadSHA: "rejected-head",
		Findings: []reviewcontract.Finding{{
			ID:             "finding-1",
			Severity:       reviewcontract.SevImportant,
			Category:       "correctness",
			Title:          "Preserve recovery finding fields",
			Detail:         "The correction worker needs the full structured finding.",
			Evidence:       []reviewcontract.Evidence{{File: "pkg/example.go", LineStart: 42, LineEnd: 43}},
			ContractImpact: reviewcontract.ContractAcceptanceGap,
			RequiredAction: "Update the acceptance contract.",
		}},
		FindingsRef:    &protocol.ReviewRecoveryArtifactRef{Path: "/state/recovery.json", SHA256: "abc", FindingCount: 1},
		Attempt:        2,
		AcceptanceHash: "acceptance",
	}

	data, err := json.Marshal(protocol.AssignPayload{BeadID: "oro-contract", Worktree: "/tmp/oro-contract", ReviewRecovery: want})
	if err != nil {
		t.Fatalf("marshal assignment recovery: %v", err)
	}
	var got protocol.AssignPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal assignment recovery: %v", err)
	}
	if !reflect.DeepEqual(got.ReviewRecovery, want) {
		t.Fatalf("recovery round trip = %#v, want %#v", got.ReviewRecovery, want)
	}
}
