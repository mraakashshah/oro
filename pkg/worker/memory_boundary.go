package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

const (
	memoryExtractionModel = "haiku"
	memoryExtractTimeout  = 30 * time.Second
	maxMemorySessionBytes = 50_000
)

const memoryExtractionPrompt = `You are a learning extractor. Given a worker session log, identify 0-5 genuine
discoveries worth remembering for future sessions. Only extract non-obvious
insights — things a developer working on this codebase would benefit from knowing.

Categories:
- lesson: something that worked or a technique discovered
- gotcha: something surprising or counterintuitive
- decision: an architectural choice and why it was made
- pattern: a reusable approach that emerged

For each discovery, output exactly one line in this format:
[MEMORY] type=<type> tags=<comma-separated>: <concise description>

If the session contains no genuine learnings (routine coding, straightforward
fixes), output nothing. Most sessions will have 0-2 learnings. Do not fabricate.

Session log (last ~12K tokens):
`

var memoryMarkerRe = regexp.MustCompile(`^\[MEMORY\]\s+type=(\w+)(?:\s+tags=([^\s:]*))?:\s+(.+)$`)

// ParseMemoryMarker extracts a memory from a [MEMORY] marker line.
// Returns nil if the line doesn't contain a valid marker.
func ParseMemoryMarker(line string) *protocol.MemoryInsertParams {
	matches := memoryMarkerRe.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	var tags []string
	if matches[2] != "" {
		tags = strings.Split(matches[2], ",")
	} else if strings.Contains(line, " tags=") {
		tags = []string{}
	}

	return &protocol.MemoryInsertParams{
		Content:    matches[3],
		Type:       matches[1],
		Tags:       tags,
		Source:     "self_report",
		Confidence: 0.8,
	}
}

// ExtractMemoriesWithLLMInWorkdir runs best-effort LLM extraction over session
// text and inserts any returned memory markers into store.
func ExtractMemoriesWithLLMInWorkdir(_ context.Context, spawner MemoryExtractSpawner, sessionText, beadID string, store LearningSink, workdir string) error {
	if spawner == nil || store == nil || sessionText == "" {
		return nil
	}
	if len(sessionText) > maxMemorySessionBytes {
		sessionText = sessionText[len(sessionText)-maxMemorySessionBytes:]
	}

	extractCtx, cancel := context.WithTimeout(context.Background(), memoryExtractTimeout)
	defer cancel()

	reader, err := spawnMemoryExtractor(extractCtx, spawner, memoryExtractionModel, memoryExtractionPrompt+sessionText, workdir)
	if err != nil {
		log.Printf("worker memory extract: spawn error: %v", err)
		return nil
	}
	defer func() { _ = reader.Close() }()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		appendMemoryMarker(extractCtx, store, beadID, scanner.Text(), 0.7)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("worker memory extract: scan error: %v", err)
	}
	return nil
}

func appendMemoryMarker(ctx context.Context, store LearningSink, beadID, line string, confidence float64) {
	if store == nil || beadID == "" {
		return
	}
	params := ParseMemoryMarker(line)
	if params == nil {
		return
	}
	candidate := cardCandidateFromMemoryMarker(*params, line, confidence)
	if _, err := store.AppendLearningPending(ctx, beadID, candidate); err != nil {
		log.Printf("worker memory extract: append pending learning error: %v", err)
	}
}

func cardCandidateFromMemoryMarker(params protocol.MemoryInsertParams, markerLine string, confidence float64) cards.CardCandidate {
	cardType := string(cards.CardTypePattern)
	if params.Type == string(cards.CardTypeDecision) {
		cardType = string(cards.CardTypeDecision)
	}
	summary := truncateForCandidate(params.Content, 200)
	tags := append([]string{}, params.Tags...)
	return cards.CardCandidate{
		Type:        cardType,
		Title:       summary,
		BodySummary: summary,
		BodyFull:    params.Content,
		Confidence:  confidence,
		Evidence:    []string{markerLine},
		Tags:        tags,
	}
}

func truncateForCandidate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	return strings.TrimSpace(s[:limit])
}

func spawnMemoryExtractor(ctx context.Context, spawner MemoryExtractSpawner, model, prompt, workdir string) (io.ReadCloser, error) {
	if workdir != "" {
		if workdirSpawner, ok := spawner.(WorkdirMemoryExtractSpawner); ok {
			reader, err := workdirSpawner.SpawnInWorkdir(ctx, model, prompt, workdir)
			if err != nil {
				return nil, fmt.Errorf("spawn extractor in workdir: %w", err)
			}
			return reader, nil
		}
	}
	reader, err := spawner.Spawn(ctx, model, prompt)
	if err != nil {
		return nil, fmt.Errorf("spawn extractor: %w", err)
	}
	return reader, nil
}
