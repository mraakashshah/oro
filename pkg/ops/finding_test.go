package ops //nolint:testpackage // tests unexported canonical helpers from the finding spine

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

func TestFindingID_StableAcrossEvidenceReorder(t *testing.T) {
	finding := Finding{
		Category: "correctness",
		Title:    "Worker loses review status",
		Evidence: []Evidence{
			{File: "pkg/worker/worker.go", LineStart: 42, LineEnd: 48, Quote: "status"},
			{File: "pkg/worker/drain.go", LineStart: 12, LineEnd: 14},
		},
	}
	reordered := finding
	reordered.Evidence = []Evidence{
		finding.Evidence[1],
		finding.Evidence[0],
	}

	if got, want := FindingID("oro-123", finding), FindingID("oro-123", reordered); got != want {
		t.Fatalf("FindingID changed after evidence reorder: got %q want %q", got, want)
	}
}

func TestFindingID_ChangesOnTitleOrCategoryOrFile(t *testing.T) {
	base := Finding{
		Category: "correctness",
		Title:    "Worker loses review status",
		Evidence: []Evidence{
			{File: "pkg/worker/worker.go", LineStart: 42, LineEnd: 48, Quote: "status"},
		},
	}
	baseID := FindingID("oro-123", base)

	cases := []struct {
		name   string
		mutate func(Finding) Finding
	}{
		{
			name: "title",
			mutate: func(f Finding) Finding {
				f.Title = "Worker drops review status"
				return f
			},
		},
		{
			name: "category",
			mutate: func(f Finding) Finding {
				f.Category = "architecture"
				return f
			},
		},
		{
			name: "file",
			mutate: func(f Finding) Finding {
				f.Evidence[0].File = "pkg/worker/drain.go"
				return f
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.mutate(base)
			if got := FindingID("oro-123", changed); got == baseID {
				t.Fatalf("FindingID did not change after changing %s: %q", tc.name, got)
			}
		})
	}
}

func TestPersistFindings_WritesJourneyRows(t *testing.T) {
	ctx := context.Background()
	store := newFindingTestStore(t)
	const beadID = "oro-review"
	if _, err := store.Create(ctx, beadstore.CreateParams{ID: beadID, Title: "review bead"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reports := []ReviewReport{
		{
			Reviewer: "persona:correctness",
			Verdict:  VerdictApproved,
			Findings: []Finding{
				persistedFinding("pkg/ops/finding.go", 10, "first issue"),
			},
		},
		{
			Reviewer: "persona:security",
			Verdict:  VerdictApproved,
			Findings: []Finding{
				persistedFinding("pkg/ops/finding.go", 20, "second issue"),
			},
		},
	}

	result := mergeReportResults(reports, reviewMergeManifest(), ReviewOpts{
		BeadID:          beadID,
		Worktree:        ".",
		PersistFindings: true,
		BeadStore:       store,
	})
	if result.Err != nil {
		t.Fatalf("mergeReports: %v", result.Err)
	}

	events, err := store.LatestJourney(ctx, beadID, 10)
	if err != nil {
		t.Fatalf("LatestJourney: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("journey events = %d, want 2: %+v", len(events), events)
	}
	for _, evt := range events {
		if evt.Actor != "ops_review" || evt.Event != "review_finding" {
			t.Fatalf("event = (%q, %q), want ops_review review_finding", evt.Actor, evt.Event)
		}
		var finding Finding
		if err := json.Unmarshal([]byte(evt.Payload), &finding); err != nil {
			t.Fatalf("payload is not JSON Finding: %v\n%s", err, evt.Payload)
		}
		if finding.ID == "" || finding.ID != FindingID(beadID, finding) {
			t.Fatalf("finding id = %q, want content-addressed id", finding.ID)
		}
	}
}

func TestReReviewPreservesTriage(t *testing.T) {
	ctx := context.Background()
	store := newFindingTestStore(t)
	const beadID = "oro-review"
	if _, err := store.Create(ctx, beadstore.CreateParams{ID: beadID, Title: "review bead"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	incoming := persistedFinding("pkg/ops/finding.go", 10, "same issue")
	incoming.Detail = "refreshed explanation"
	incoming.Confidence = 100
	incoming.ID = FindingID(beadID, incoming)

	prior := incoming
	prior.Detail = "stale explanation"
	prior.Confidence = 75
	prior.Status = "false-positive"
	prior.History = []FindingHistoryEntry{
		{Status: "false-positive", Actor: "maintainer", Note: "reviewed and dismissed", At: "2026-05-31T10:00:00Z"},
	}
	priorPayload, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal prior finding: %v", err)
	}
	if err := store.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Ts:      time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Actor:   "ops_review",
		Event:   "review_finding",
		Payload: string(priorPayload),
	}); err != nil {
		t.Fatalf("AppendJourney prior: %v", err)
	}

	result := mergeReportResults([]ReviewReport{{
		Reviewer: "persona:correctness",
		Verdict:  VerdictRejected,
		Findings: []Finding{incoming},
	}}, reviewMergeManifest(), ReviewOpts{
		BeadID:          beadID,
		Worktree:        ".",
		PersistFindings: true,
		BeadStore:       store,
	})
	if result.Err != nil {
		t.Fatalf("mergeReports: %v", result.Err)
	}
	if result.Verdict != VerdictApproved {
		t.Fatalf("verdict = %q, want %q for triaged false-positive", result.Verdict, VerdictApproved)
	}
	if findings := reviewMergeFeedbackFindings(t, result); len(findings) != 0 {
		t.Fatalf("gate findings = %#v, want none for triaged false-positive", findings)
	}

	events, err := store.LatestJourney(ctx, beadID, 10)
	if err != nil {
		t.Fatalf("LatestJourney: %v", err)
	}
	var refreshed Finding
	if err := json.Unmarshal([]byte(events[len(events)-1].Payload), &refreshed); err != nil {
		t.Fatalf("unmarshal refreshed finding: %v", err)
	}
	if refreshed.Detail != incoming.Detail {
		t.Fatalf("detail = %q, want refreshed %q", refreshed.Detail, incoming.Detail)
	}
	if refreshed.Status != prior.Status {
		t.Fatalf("status = %q, want preserved %q", refreshed.Status, prior.Status)
	}
	if len(refreshed.History) != 1 || refreshed.History[0] != prior.History[0] {
		t.Fatalf("history = %#v, want preserved %#v", refreshed.History, prior.History)
	}
}

func newFindingTestStore(t *testing.T) *beadstore.SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := protocol.MigrateBeadSchema(context.Background(), db); err != nil {
		t.Fatalf("MigrateBeadSchema: %v", err)
	}
	if err := migrations.MigrateToV3(context.Background(), db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}
	return beadstore.NewSQLiteStore(db)
}

func persistedFinding(file string, line int, title string) Finding {
	return Finding{
		Severity:   SevImportant,
		Category:   "correctness",
		Title:      title,
		Detail:     "detail",
		Confidence: 90,
		Evidence: []Evidence{
			{File: file, LineStart: line, LineEnd: line},
		},
	}
}
