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

// DrainOutput reads subprocess stdout line by line, echoes each line to w,
// and extracts [MEMORY] markers into the memory store. Safe when store is nil.
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store MemoryInserter, beadID string, w io.Writer) {
	defer func() { _ = stdout.Close() }()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(w, line)

		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params)
			}
		}
	}
}
