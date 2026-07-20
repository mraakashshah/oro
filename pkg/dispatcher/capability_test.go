package dispatcher //nolint:testpackage // white-box test exercises unexported capability persistence helpers

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIssueAssignmentCapabilityPersistsHashWithoutBearerToken(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	assignmentID, err := d.createAssignment(ctx, "capability-bead", "capability-worker", "/tmp/capability")
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	capability, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker)
	if err != nil {
		t.Fatalf("issue assignment capability: %v", err)
	}
	if capability.Token == "" {
		t.Fatal("issued capability token is empty")
	}
	if capability.ExpiresAt.Before(time.Now().UTC().Add(19 * time.Minute)) {
		t.Fatalf("capability expiry %s is too soon", capability.ExpiresAt)
	}

	var tokenHash string
	if err := d.db.QueryRowContext(ctx, `
SELECT token_hash FROM assignment_capabilities WHERE capability_id = ?`, capability.ID,
	).Scan(&tokenHash); err != nil {
		t.Fatalf("load persisted capability: %v", err)
	}
	if tokenHash == capability.Token || strings.Contains(tokenHash, capability.Token) {
		t.Fatal("raw bearer token persisted instead of a hash")
	}
	if tokenHash == "" {
		t.Fatal("persisted token hash is empty")
	}
}

func TestIssueAssignmentCapabilityRejectsMissingAssignmentWithoutPersistingCapability(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.issueAssignmentCapability(ctx, 999, 1, ActorRoleExecutionWorker); err == nil {
		t.Fatal("issue capability for missing assignment succeeded")
	}

	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_capabilities`).Scan(&count); err != nil {
		t.Fatalf("count capabilities: %v", err)
	}
	if count != 0 {
		t.Fatalf("persisted capabilities after missing assignment = %d, want 0", count)
	}
}
