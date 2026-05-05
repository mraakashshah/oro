package beadstore_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// renderFacingReadMethods are the Store methods reachable from render paths
// (oro current, oro handoff, oro resume, oro pipeline status).
// Export is explicitly excluded per §4.7: it has its own transactional semantics.
var renderFacingReadMethods = []string{
	"Ready",
	"InProgress",
	"Blocked",
	"Closed",
	"Show",
	"HasChildren",
	"AllChildrenClosed",
	"FindByParentAndTag",
	"Journey",
	"LatestJourney",
}

// TestReadTxParity verifies:
//  1. Every render-facing read method on Store exists on ReadTx with an identical signature.
//  2. Cards reads inside WithReadTx share the same SQL transaction as bead reads.
func TestReadTxParity(t *testing.T) {
	t.Run("method_parity", func(t *testing.T) {
		storeType := reflect.TypeOf((*beadstore.Store)(nil)).Elem()
		txType := reflect.TypeOf((*beadstore.ReadTx)(nil)).Elem()

		for _, name := range renderFacingReadMethods {
			storeMethod, ok := storeType.MethodByName(name)
			if !ok {
				t.Errorf("Store.%s does not exist (update renderFacingReadMethods)", name)
				continue
			}
			txMethod, ok := txType.MethodByName(name)
			if !ok {
				t.Errorf("ReadTx.%s is missing", name)
				continue
			}
			if storeMethod.Type != txMethod.Type {
				t.Errorf("ReadTx.%s signature mismatch:\n  Store has %v\n  ReadTx has %v",
					name, storeMethod.Type, txMethod.Type)
			}
		}
	})

	t.Run("cross_store_cards_share_tx", func(t *testing.T) {
		ctx := context.Background()

		// Open an in-memory SQLite DB and apply both bead and cards schemas.
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		defer db.Close()

		if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("apply runtime schema: %v", err)
		}
		if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
			t.Fatalf("migrate bead schema: %v", err)
		}

		// Apply cards schema and insert a card.
		cardsStore, err := cards.NewStore(db)
		if err != nil {
			t.Fatalf("new cards store: %v", err)
		}
		card, err := cardsStore.Create(ctx, cards.CardCreateParams{
			Type:        cards.CardTypeRule,
			Title:       "test rule",
			BodySummary: "summary",
			BodyFull:    "full body",
		})
		if err != nil {
			t.Fatalf("create card: %v", err)
		}

		beadStore := beadstore.NewSQLiteStore(db)

		// Verify cards reads inside WithReadTx share the same transaction snapshot.
		var gotCard *cards.Card
		err = beadStore.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
			got, showErr := tx.Cards().Show(ctx, card.ID)
			if showErr != nil {
				return showErr
			}
			gotCard = got
			return nil
		})
		if err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}
		if gotCard == nil {
			t.Fatal("Cards().Show returned nil inside WithReadTx")
		}
		if gotCard.ID != card.ID {
			t.Errorf("Cards().Show ID = %q, want %q", gotCard.ID, card.ID)
		}
	})
}
