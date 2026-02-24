package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"oro/pkg/memory"
)

// MemoryInserter abstracts memory insertion for testing.
type MemoryInserter interface {
	Insert(ctx context.Context, m memory.InsertParams) (int64, error)
}

// DrainOutput reads subprocess stdout line by line, echoes each line to writers,
// and extracts [MEMORY] markers into the memory store. Safe when store is nil.
// Nil writers in the slice are filtered out. Empty writers slice is a no-op for output.
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store MemoryInserter, beadID string, writers ...io.Writer) {
	defer func() { _ = stdout.Close() }()

	// Filter nil writers.
	valid := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			valid = append(valid, w)
		}
	}
	var out io.Writer
	if len(valid) > 0 {
		out = io.MultiWriter(valid...)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if out != nil {
			fmt.Fprintln(out, line)
		}

		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params)
			}
		}
	}
}
