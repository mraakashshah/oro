//go:build migration

package fixture

import "context"

// migrate: store.Close in a //go:build migration file — must NOT be flagged.
func migrate(ctx context.Context, store Store) {
	_ = store.Close(ctx, "id", "reason")
}
