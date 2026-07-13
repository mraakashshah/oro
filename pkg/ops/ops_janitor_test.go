package ops //nolint:testpackage // internal test needs access to parseResult

import (
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestJanitorAuditTypeWiring(t *testing.T) {
	cases := []struct {
		name        string
		typ         Type
		wantTier    protocol.Tier
		wantTimeout time.Duration
		wantRole    string
	}{
		{
			name:        "janitor",
			typ:         OpsJanitor,
			wantTier:    protocol.TierFast,
			wantTimeout: 10 * time.Minute,
			wantRole:    "ops_janitor",
		},
		{
			name:        "audit",
			typ:         OpsAudit,
			wantTier:    protocol.TierDeep,
			wantTimeout: 20 * time.Minute,
			wantRole:    "ops_audit",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.typ.Tier(); got != tc.wantTier {
				t.Fatalf("Tier() = %q, want %q", got, tc.wantTier)
			}
			if got := tc.typ.Role(); got != tc.wantRole {
				t.Fatalf("Role() = %q, want %q", got, tc.wantRole)
			}
			if got := tc.typ.Timeout(); got != tc.wantTimeout {
				t.Fatalf("Timeout() = %v, want %v", got, tc.wantTimeout)
			}

			feedback := "raw fixture stdout for " + tc.name
			if got := parseResult(tc.typ, "oro-test", feedback, nil).Feedback; got != feedback {
				t.Fatalf("parseResult() feedback = %q, want %q", got, feedback)
			}
		})
	}
}
