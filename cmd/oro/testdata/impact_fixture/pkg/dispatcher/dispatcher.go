package dispatcher

import (
	"context"
	"fmt"

	"fixture/pkg/worker"
)

// Dispatcher orchestrates workers.
type Dispatcher struct{}

// Run executes the dispatcher loop.
func (d *Dispatcher) Run(ctx context.Context) error {
	msg := worker.Assemble()
	ctx, cancel := context.WithTimeout(ctx, 0)
	defer cancel()
	fmt.Println(msg)
	return nil
}
