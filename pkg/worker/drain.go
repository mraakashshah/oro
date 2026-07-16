package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var credentialAssignmentRE = regexp.MustCompile(`(?i)\b(?:ANTHROPIC_API_KEY|CLAUDE_CODE_OAUTH_TOKEN|OPENAI_API_KEY)\s*=`)

// DrainOutput reads subprocess stdout according to format, writes formatted
// activity and/or text content to writers, and extracts [MEMORY] markers
// from text in real time. After the stream is fully drained, it runs
// LLM-based extraction on the accumulated text.
// Safe when store or spawner is nil. Nil writers in the slice are filtered out.
// Empty writers slice is a no-op for output.
//
//oro:testonly
func DrainOutput(ctx context.Context, stdout io.ReadCloser, format StreamFormat, store LearningSink, beadID string, spawner MemoryExtractSpawner, writers ...io.Writer) {
	DrainOutputInWorkdir(ctx, stdout, format, store, beadID, spawner, "", writers...)
}

// DrainOutputInWorkdir is DrainOutput with a worktree binding for post-drain
// LLM memory extraction subprocesses.
func DrainOutputInWorkdir(ctx context.Context, stdout io.ReadCloser, format StreamFormat, store LearningSink, beadID string, spawner MemoryExtractSpawner, workdir string, writers ...io.Writer) {
	defer func() { _ = stdout.Close() }()

	out := filterWriters(writers)

	var accumulated strings.Builder
	var sanitizer credentialLineSanitizer

	var totalLines, unknownLines int
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		totalLines++
		switch format {
		case StreamFormatLineText:
			drainProcessSanitizedLine(ctx, out, scanner.Text(), &accumulated, store, beadID)
		default:
			activity := ParseStreamEvent(scanner.Bytes())
			if activity.Kind == ActivityUnknown {
				unknownLines++
			}
			drainWriteActivity(out, activity)
			for _, line := range sanitizer.append(activity.Text) {
				drainProcessSanitizedLine(ctx, out, line, &accumulated, store, beadID)
			}
		}
	}
	if err := scanner.Err(); err != nil && out != nil {
		fmt.Fprintf(out, "--- Scanner error: %v\n", err)
	}
	if out != nil {
		fmt.Fprintf(out, "--- Stream stats: %d lines (%d unknown)\n", totalLines, unknownLines)
	}

	if format != StreamFormatLineText {
		if line, ok := sanitizer.flush(); ok {
			drainProcessSanitizedLine(ctx, out, line, &accumulated, store, beadID)
		}
	}

	// Post-drain LLM extraction on accumulated session text.
	if spawner != nil && store != nil {
		_ = ExtractMemoriesWithLLMInWorkdir(ctx, spawner, accumulated.String(), beadID, store, workdir)
	}
}

func drainProcessSanitizedLine(ctx context.Context, out io.Writer, line string, accumulated *strings.Builder, store LearningSink, beadID string) {
	line = redactCredentialAssignments(line)
	drainWritePlaintext(out, line)
	drainAccumulatePlaintext(ctx, line, accumulated, store, beadID)
}

// redactCredentialAssignments masks values assigned to supported credential
// environment variables while preserving surrounding text and quote wrappers.
func redactCredentialAssignments(text string) string {
	indices := credentialAssignmentRE.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return text
	}

	var redacted strings.Builder
	redacted.Grow(len(text))
	last := 0
	for _, index := range indices {
		assignmentEnd := index[1]
		if assignmentEnd < last {
			continue
		}
		valueStart, valueEnd := credentialValueBounds(text, assignmentEnd)
		redacted.WriteString(text[last:assignmentEnd])
		redacted.WriteString(text[assignmentEnd:valueStart])
		redacted.WriteString("[REDACTED]")
		last = valueEnd
	}
	redacted.WriteString(text[last:])
	return redacted.String()
}

// credentialLineSanitizer reassembles structured text deltas before values are
// redacted. This prevents split credential assignments from reaching a sink.
type credentialLineSanitizer struct {
	pending strings.Builder
}

func (s *credentialLineSanitizer) append(text string) []string {
	if text == "" {
		return nil
	}
	s.pending.WriteString(text)
	content := s.pending.String()
	lastNL := strings.LastIndex(content, "\n")
	if lastNL < 0 {
		return nil
	}
	complete := content[:lastNL]
	s.pending.Reset()
	s.pending.WriteString(content[lastNL+1:])
	return strings.Split(complete, "\n")
}

func (s *credentialLineSanitizer) flush() (string, bool) {
	if s.pending.Len() == 0 {
		return "", false
	}
	line := s.pending.String()
	s.pending.Reset()
	return line, true
}

func credentialValueBounds(text string, start int) (valueStart, valueEnd int) {
	if start >= len(text) {
		return start, start
	}
	if strings.HasPrefix(text[start:], `\"`) {
		return start + 2, escapedCredentialValueEnd(text, start)
	}
	if text[start] == '"' || text[start] == '\'' {
		return start + 1, quotedCredentialValueEnd(text, start)
	}
	for end := start; end < len(text); end++ {
		if text[end] == ' ' || text[end] == '\t' || text[end] == '\n' || text[end] == '\r' {
			return start, end
		}
	}
	return start, len(text)
}

func escapedCredentialValueEnd(text string, start int) int {
	for quote := start + 2; quote < len(text); quote++ {
		if text[quote] == '"' && hasOddTrailingBackslashes(text, quote) {
			return quote - 1
		}
	}
	return len(text)
}

func quotedCredentialValueEnd(text string, start int) int {
	quote := text[start]
	for end := start + 1; end < len(text); end++ {
		if text[end] == quote && !hasOddTrailingBackslashes(text, end) {
			return end
		}
	}
	return len(text)
}

func hasOddTrailingBackslashes(text string, end int) bool {
	count := 0
	for before := end - 1; before >= 0 && text[before] == '\\'; before-- {
		count++
	}
	return count%2 == 1
}

func drainWritePlaintext(out io.Writer, line string) {
	if out == nil {
		return
	}
	_, _ = io.WriteString(out, line)
	_, _ = io.WriteString(out, "\n")
}

func drainAccumulatePlaintext(ctx context.Context, line string, accumulated *strings.Builder, store LearningSink, beadID string) {
	accumulated.WriteString(line)
	accumulated.WriteString("\n")
	appendMemoryMarker(ctx, store, beadID, line)
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

// drainWriteActivity writes formatted tool activity to the output writer.
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
}
