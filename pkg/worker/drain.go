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

// DrainOutput reads subprocess stdout line by line (NDJSON from
// --output-format stream-json), parses each event, writes formatted
// tool-call activity to writers, and extracts [MEMORY] markers from text
// content in real time. After the stream is fully drained, it runs
// LLM-based extraction on the accumulated text via memory.ExtractWithLLM.
// Safe when store or spawner is nil.
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
	var lineBuf strings.Builder

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		activity := ParseStreamEvent(scanner.Bytes())

		// Write formatted tool-call activity to output writers.
		if formatted := FormatActivity(activity); formatted != "" && out != nil {
			fmt.Fprintln(out, formatted)
		}

		// Accumulate text content and process complete lines.
		if activity.Text != "" {
			lineBuf.WriteString(activity.Text)
			drainFlushLines(ctx, &lineBuf, &accumulated, store, beadID)
		}
	}

	// Flush any remaining buffered text.
	if lineBuf.Len() > 0 {
		remaining := lineBuf.String()
		accumulated.WriteString(remaining)
		accumulated.WriteString("\n")
		if store != nil {
			if params := memory.ParseMarker(remaining); params != nil {
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

// drainFlushLines extracts complete newline-terminated lines from buf,
// appends each to accumulated, and checks for [MEMORY] markers.
func drainFlushLines(ctx context.Context, buf, accumulated *strings.Builder, store MemoryInserter, beadID string) {
	content := buf.String()
	lastNL := strings.LastIndex(content, "\n")
	if lastNL < 0 {
		return
	}
	complete := content[:lastNL]
	buf.Reset()
	if lastNL+1 < len(content) {
		buf.WriteString(content[lastNL+1:])
	}
	for _, line := range strings.Split(complete, "\n") {
		accumulated.WriteString(line)
		accumulated.WriteString("\n")
		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params)
			}
		}
	}
}
