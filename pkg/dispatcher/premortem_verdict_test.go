package dispatcher //nolint:testpackage // white-box: needs Dispatcher fields

import (
	"context"
	"database/sql"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// TestPremortemVerdictPersistedAtClose verifies §11.4 contract:
// when a premortem bead closes, its verdict payload is persisted in
// bead.Metadata. Missing/malformed payloads default to "replan" with a
// warning event logged. Verdict ∈ {proceed, block, replan}; reason text
// is preserved verbatim.
func TestPremortemVerdictPersistedAtClose(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name        string
		payload     []byte
		wantVerdict string
		wantReason  string
		wantWarning bool
	}{
		{
			name:        "proceed_verdict_persisted",
			payload:     []byte(`{"verdict":"proceed","reason":"low risk; deps available"}`),
			wantVerdict: "proceed",
			wantReason:  "low risk; deps available",
			wantWarning: false,
		},
		{
			name:        "block_verdict_persisted",
			payload:     []byte(`{"verdict":"block","reason":"missing API key for OAuth"}`),
			wantVerdict: "block",
			wantReason:  "missing API key for OAuth",
			wantWarning: false,
		},
		{
			name:        "replan_verdict_persisted",
			payload:     []byte(`{"verdict":"replan","reason":"need decomposition"}`),
			wantVerdict: "replan",
			wantReason:  "need decomposition",
			wantWarning: false,
		},
		{
			name:        "missing_payload_defaults_to_replan_with_warning",
			payload:     nil,
			wantVerdict: "replan",
			wantReason:  "",
			wantWarning: true,
		},
		{
			name:        "empty_payload_defaults_to_replan_with_warning",
			payload:     []byte(``),
			wantVerdict: "replan",
			wantReason:  "",
			wantWarning: true,
		},
		{
			name:        "malformed_json_defaults_to_replan_with_warning",
			payload:     []byte(`{not json`),
			wantVerdict: "replan",
			wantReason:  "",
			wantWarning: true,
		},
		{
			name:        "unknown_verdict_value_defaults_to_replan_with_warning",
			payload:     []byte(`{"verdict":"sneaky","reason":"x"}`),
			wantVerdict: "replan",
			wantReason:  "x",
			wantWarning: true,
		},
		{
			name:        "empty_verdict_field_defaults_to_replan_with_warning",
			payload:     []byte(`{"verdict":"","reason":"y"}`),
			wantVerdict: "replan",
			wantReason:  "y",
			wantWarning: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadID := "pm-" + tc.name
			store := beadstore.NewFakeStore(protocol.Bead{
				ID:     beadID,
				Type:   "premortem",
				Status: "open",
				Epic:   "epic-target",
			})
			db := newTestDB(t)
			d := &Dispatcher{beads: store, db: db}

			err := d.ClosePremortemBead(ctx, beadID, tc.payload)
			if err != nil {
				t.Fatalf("ClosePremortemBead: unexpected error: %v", err)
			}

			got, err := store.Show(ctx, beadID)
			if err != nil {
				t.Fatalf("Show after close: %v", err)
			}
			if got == nil {
				t.Fatal("bead not found after close")
			}
			if got.Status != "closed" {
				t.Errorf("status: want closed, got %q", got.Status)
			}

			gotVerdict, _ := got.Metadata["premortem_verdict"].(string)
			if gotVerdict != tc.wantVerdict {
				t.Errorf("metadata[premortem_verdict]: want %q, got %q", tc.wantVerdict, gotVerdict)
			}
			gotReason, _ := got.Metadata["premortem_reason"].(string)
			if gotReason != tc.wantReason {
				t.Errorf("metadata[premortem_reason]: want %q, got %q", tc.wantReason, gotReason)
			}

			gotWarning := warningLogged(t, db, beadID)
			if gotWarning != tc.wantWarning {
				t.Errorf("warning event logged: want %v, got %v", tc.wantWarning, gotWarning)
			}
		})
	}
}

// warningLogged reports whether a premortem_verdict_invalid event was recorded
// for beadID in the dispatcher's events table.
func warningLogged(t *testing.T, db *sql.DB, beadID string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM events WHERE type='premortem_verdict_invalid' AND bead_id=?`,
		beadID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	return count > 0
}
