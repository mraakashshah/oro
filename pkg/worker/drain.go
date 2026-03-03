package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"oro/pkg/memory"
)

// MemoryInserter abstracts memory insertion for testing.
type MemoryInserter interface {
	Insert(ctx context.Context, m memory.InsertParams) (int64, error)
}

// DrainOutput reads subprocess stdout line by line, echoes each line to writers,
// and extracts [MEMORY] markers into the memory store in real time. After the
// stream is fully drained, it runs LLM-based extraction on the accumulated text
// via memory.ExtractWithLLM. Safe when store or spawner is nil.
// Nil writers in the slice are filtered out. Empty writers slice is a no-op for output.
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store MemoryInserter, beadID string, spawner memory.Spawner, writers ...io.Writer) {
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

	var accumulated strings.Builder

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if out != nil {
			fmt.Fprintln(out, line)
		}

		accumulated.WriteString(line)
		accumulated.WriteString("\n")

		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params)
			}
		}
	}

	// Post-drain LLM extraction on accumulated session text.
	if spawner != nil && store != nil {
		_ = memory.ExtractWithLLM(ctx, spawner, accumulated.String(), beadID, store)
	}
}
