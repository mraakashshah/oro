package caller

import (
	"context"

	"fixture/pkg/dispatcher"
)

// StartAll runs the given dispatcher.
func StartAll(d *dispatcher.Dispatcher) {
	_ = d.Run(context.Background())
}
