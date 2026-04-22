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

// DrainOutput reads subprocess stdout according to format, writes formatted
// activity and/or text content to writers, and extracts [MEMORY] markers
// from text in real time. After the stream is fully drained, it runs
// LLM-based extraction on the accumulated text via memory.ExtractWithLLM.
// Safe when store or spawner is nil. Nil writers in the slice are filtered out.
// Empty writers slice is a no-op for output.
func DrainOutput(ctx context.Context, stdout io.ReadCloser, format StreamFormat, store MemoryInserter, beadID string, spawner memory.Spawner, writers ...io.Writer) {
	defer func() { _ = stdout.Close() }()

	out := filterWriters(writers)

	var accumulated strings.Builder
	var lineBuf strings.Builder

	var totalLines, unknownLines int
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		totalLines++
		switch format {
		case StreamFormatLineText:
			drainWritePlaintext(out, scanner.Text())
			drainAccumulatePlaintext(ctx, scanner.Text(), &accumulated, store, beadID)
		default:
			activity := ParseStreamEvent(scanner.Bytes())
			if activity.Kind == ActivityUnknown {
				unknownLines++
			}
			drainWriteActivity(out, activity)
			drainAccumulateText(ctx, activity, &lineBuf, &accumulated, store, beadID)
		}
	}
	if err := scanner.Err(); err != nil && out != nil {
		fmt.Fprintf(out, "--- Scanner error: %v\n", err)
	}
	if out != nil {
		fmt.Fprintf(out, "--- Stream stats: %d lines (%d unknown)\n", totalLines, unknownLines)
	}

	if format != StreamFormatLineText {
		drainFlushRemaining(ctx, &lineBuf, &accumulated, store, beadID)
	}

	// Post-drain LLM extraction on accumulated session text.
	if spawner != nil && store != nil {
		_ = memory.ExtractWithLLM(ctx, spawner, accumulated.String(), beadID, store)
	}
}

func drainWritePlaintext(out io.Writer, line string) {
	if out == nil {
		return
	}
	_, _ = io.WriteString(out, line)
	_, _ = io.WriteString(out, "\n")
}

func drainAccumulatePlaintext(ctx context.Context, line string, accumulated *strings.Builder, store MemoryInserter, beadID string) {
	accumulated.WriteString(line)
	accumulated.WriteString("\n")
	if store != nil {
		if params := memory.ParseMarker(line); params != nil {
			params.BeadID = beadID
			_, _ = store.Insert(ctx, *params)
		}
	}
}

// filterWriters returns a multi-writer for non-nil writers, or nil.
func filterWriters(writers []io.Writer) io.Writer {
	valid := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			valid = append(valid, w)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	return io.MultiWriter(valid...)
}

// drainWriteActivity writes formatted tool activity and text to the output writer.
func drainWriteActivity(out io.Writer, activity Activity) {
	if out == nil {
		return
	}
	if formatted := FormatActivity(activity); formatted != "" {
		fmt.Fprintln(out, formatted)
	}
	if resultSummary := FormatResult(activity); resultSummary != "" {
		fmt.Fprintln(out, resultSummary)
	}
	if activity.Text != "" {
		_, _ = io.WriteString(out, activity.Text)
	}
}

// drainAccumulateText buffers text content and flushes complete lines for memory extraction.
func drainAccumulateText(ctx context.Context, activity Activity, lineBuf, accumulated *strings.Builder, store MemoryInserter, beadID string) {
	if activity.Text == "" {
		return
	}
	lineBuf.WriteString(activity.Text)
	drainFlushLines(ctx, lineBuf, accumulated, store, beadID)
}

// drainFlushRemaining flushes any buffered text that doesn't end with a newline.
func drainFlushRemaining(ctx context.Context, lineBuf, accumulated *strings.Builder, store MemoryInserter, beadID string) {
	if lineBuf.Len() == 0 {
		return
	}
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
