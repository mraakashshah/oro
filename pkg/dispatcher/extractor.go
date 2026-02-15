package dispatcher

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"oro/pkg/memory"
)

// extractionPatterns maps regex patterns to memory types for daemon-side
// learning extraction. These patterns capture insights mentioned in session
// logs that agents didn't explicitly tag with [MEMORY] markers.
//
//nolint:gochecknoglobals // compile-once regex table, safe as package-level var
var extractionPatterns = []struct {
	re      *regexp.Regexp
	memType string
}{
	// Explicit learning signals
	{regexp.MustCompile(`(?i)I learned\s+that\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^Note:\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^Gotcha:\s+(.+?)\.?\s*$`), "gotcha"},
	{regexp.MustCompile(`(?i)^Important:\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^TIL:\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^Pattern:\s+(.+?)\.?\s*$`), "pattern"},
	{regexp.MustCompile(`(?i)^Decision:\s+(.+?)\.?\s*$`), "decision"},
	{regexp.MustCompile(`(?i)^Decided:\s+(.+?)\.?\s*$`), "decision"},

	// Diagnostic signals (failure/resolution pairs)
	{regexp.MustCompile(`(?i)This doesn'?t work because\s+(.+?)\.?\s*$`), "gotcha"},
	{regexp.MustCompile(`(?i)^The fix was\s+(.+?)\.?\s*$`), "lesson"},
}

// extractedConfidence is the confidence score for daemon-extracted learnings,
// lower than self-report (0.8) to reflect that these are inferred, not explicit.
const extractedConfidence = 0.6

// ExtractLearnings scans text for learning patterns and produces memory entries.
// Each match becomes an InsertParams with type="daemon_extracted" source,
// the given beadID, and a confidence of 0.6.
//
// Duplicate content within the same call is deduplicated by normalized content.
func ExtractLearnings(text, beadID string) []memory.InsertParams {
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	seen := make(map[string]struct{})
	var results []memory.InsertParams

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, p := range extractionPatterns {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			content := strings.TrimSpace(m[1])
			// Strip trailing punctuation that the lazy regex may leave.
			content = strings.TrimRight(content, ".")
			content = strings.TrimSpace(content)
			if content == "" {
				continue
			}

			// Deduplicate by lowercased content within this extraction run.
			key := strings.ToLower(content)
			if _, dup := seen[key]; dup {
				break
			}
			seen[key] = struct{}{}

			results = append(results, memory.InsertParams{
				Content:    content,
				Type:       p.memType,
				Source:     "daemon_extracted",
				BeadID:     beadID,
				Confidence: extractedConfidence,
			})
			break // one match per line
		}
	}

	return results
}

// extractAndStoreLearnings collects event payloads for a completed bead,
// runs ExtractLearnings over the combined text, and inserts any discovered
// memories into the store. Best-effort: errors are logged but do not propagate.
func (d *Dispatcher) extractAndStoreLearnings(ctx context.Context, beadID string) {
	if d.memories == nil || d.db == nil {
		return
	}

	// Collect all event payloads for this bead.
	rows, err := d.db.QueryContext(ctx,
		`SELECT payload FROM events WHERE bead_id = ? AND payload != '' ORDER BY id`, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "extract_learnings_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	defer func() { _ = rows.Close() }()

	var buf strings.Builder
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		buf.WriteString(payload)
		buf.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return
	}

	text := buf.String()
	if text == "" {
		return
	}

	entries := ExtractLearnings(text, beadID)
	if len(entries) == 0 {
		return
	}

	inserted := 0
	for _, entry := range entries {
		if _, err := d.memories.Insert(ctx, entry); err != nil {
			continue
		}
		inserted++
	}

	if inserted > 0 {
		_ = d.logEvent(ctx, "learnings_extracted", "dispatcher", beadID, "",
			fmt.Sprintf(`{"count":%d}`, inserted))
	}
}
