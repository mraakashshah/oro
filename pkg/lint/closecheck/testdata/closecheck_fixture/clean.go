package fixture

import "context"

// CloseBead: the blessed wrapper — its body must NOT be flagged.
func (d *Dispatcher) CloseBead(ctx context.Context, id, reason string) error {
	return d.beads.Close(ctx, id, reason)
}

// callCloseBead: calls Dispatcher.CloseBead — must NOT be flagged.
func (d *Dispatcher) callCloseBead(ctx context.Context, id, reason string) error {
	return d.CloseBead(ctx, id, reason)
}
