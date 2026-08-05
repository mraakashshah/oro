package beadstore

import (
	"context"
	"strings"
	"testing"
)

func TestUpdateReportsTransactionStartFailure(t *testing.T) {
	store := newTestSQLiteStore(t)
	if err := store.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	title := "unreachable"
	err := store.Update(context.Background(), "missing", UpdateParams{Title: &title})
	if err == nil || !strings.Contains(err.Error(), "begin update transaction") {
		t.Fatalf("Update err = %v, want transaction start failure", err)
	}
}

func TestUpdateReportsStatementFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	mustCreate(t, store, CreateParams{ID: "oro-update-trigger", Title: "before"})
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_bead_update
		BEFORE UPDATE ON beads BEGIN
			SELECT RAISE(ABORT, 'injected update failure');
		END`); err != nil {
		t.Fatalf("create update trigger: %v", err)
	}

	title := "after"
	err := store.Update(ctx, "oro-update-trigger", UpdateParams{Title: &title})
	if err == nil || !strings.Contains(err.Error(), "injected update failure") {
		t.Fatalf("Update err = %v, want injected statement failure", err)
	}
}

func TestUpdateRollsBackAtEveryPostUpdateFailureBoundary(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		trigger string
		params  func(string) UpdateParams
		journey *JourneyEvent
	}{
		{
			name: "side effects",
			trigger: `CREATE TRIGGER reject_tag_insert BEFORE INSERT ON bead_tags BEGIN
				SELECT RAISE(ABORT, 'injected side-effect failure');
			END`,
			params: func(title string) UpdateParams {
				tags := []string{"new-tag"}
				return UpdateParams{Title: &title, Tags: &tags}
			},
		},
		{
			name: "event",
			trigger: `CREATE TRIGGER reject_update_event BEFORE INSERT ON events
				WHEN NEW.type = 'bead_updated' BEGIN
					SELECT RAISE(ABORT, 'injected event failure');
				END`,
			params: func(title string) UpdateParams { return UpdateParams{Title: &title} },
		},
		{
			name: "journey",
			trigger: `CREATE TRIGGER reject_update_journey BEFORE INSERT ON bead_journey BEGIN
				SELECT RAISE(ABORT, 'injected journey failure');
			END`,
			params: func(title string) UpdateParams { return UpdateParams{Title: &title} },
			journey: &JourneyEvent{
				Ts:      "2026-01-01T00:00:00Z",
				Actor:   "mutation-test",
				Event:   "updated",
				Payload: `{"source":"test"}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestSQLiteStore(t)
			mustCreate(t, store, CreateParams{ID: "oro-update-rollback", Title: "before"})
			if tc.journey != nil {
				if _, err := store.db.ExecContext(ctx, `CREATE TABLE bead_journey (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					bead_id TEXT NOT NULL,
					ts TEXT NOT NULL,
					actor TEXT NOT NULL,
					event TEXT NOT NULL,
					payload TEXT
				)`); err != nil {
					t.Fatalf("create journey table: %v", err)
				}
			}
			if _, err := store.db.ExecContext(ctx, tc.trigger); err != nil {
				t.Fatalf("create failure trigger: %v", err)
			}

			const changedTitle = "after"
			var err error
			if tc.journey == nil {
				err = store.Update(ctx, "oro-update-rollback", tc.params(changedTitle))
			} else {
				err = store.UpdateWithJourney(ctx, "oro-update-rollback", tc.params(changedTitle), *tc.journey)
			}
			if err == nil {
				t.Fatal("update err = nil, want injected failure")
			}
			bead, showErr := store.Show(ctx, "oro-update-rollback")
			if showErr != nil {
				t.Fatalf("Show after rollback: %v", showErr)
			}
			if bead.Title != "before" {
				t.Fatalf("title = %q, want before after rollback", bead.Title)
			}
		})
	}
}

func TestUpdateRejectsInvalidStatusBeforeOpeningTransaction(t *testing.T) {
	store := newTestSQLiteStore(t)
	invalid := "definitely-not-a-status"

	err := store.Update(context.Background(), "missing", UpdateParams{Status: &invalid})
	if err == nil || !strings.Contains(err.Error(), `invalid status "definitely-not-a-status"`) {
		t.Fatalf("Update err = %v, want invalid status validation failure", err)
	}
}
