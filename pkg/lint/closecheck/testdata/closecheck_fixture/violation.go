package fixture

import "context"

// Store mirrors the beadstore.Store Close signature.
type Store interface {
	Close(ctx context.Context, id, reason string) error
}

type Dispatcher struct {
	beads Store
}

// badStore: store-named variable — should be flagged.
func badStore(ctx context.Context, store Store) {
	_ = store.Close(ctx, "id", "reason")
}

// badBeads: beads field on receiver — should be flagged.
func (d *Dispatcher) badBeads(ctx context.Context, id, reason string) {
	_ = d.beads.Close(ctx, id, reason)
}
