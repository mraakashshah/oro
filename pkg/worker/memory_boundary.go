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

	candidates, err := ExtractMemoriesFromReader(context.Background(), strings.NewReader(sessionText), spawner, workdir)
	if err != nil {
		log.Printf("worker memory extract: reader error: %v", err)
		//nolint:nilerr // Preserve historical best-effort behavior for this compatibility wrapper.
		return nil
	}
	appendMemoryCandidates(context.Background(), store, beadID, candidates)
	return nil
}

// ExtractMemoriesFromReader runs best-effort LLM extraction over a bounded
// reader source and returns card candidates parsed from memory marker output.
func ExtractMemoriesFromReader(ctx context.Context, src io.Reader, spawner MemoryExtractSpawner, workdir string) ([]cards.CardCandidate, error) {
	if spawner == nil || src == nil {
		return nil, nil
	}
	sessionBytes, err := io.ReadAll(io.LimitReader(src, maxMemorySessionBytes))
	if err != nil {
		return nil, fmt.Errorf("read memory extraction source: %w", err)
	}
	if len(sessionBytes) == 0 {
		return nil, nil
	}

	extractCtx, cancel := context.WithTimeout(ctx, memoryExtractTimeout)
	defer cancel()

	reader, err := spawnMemoryExtractor(extractCtx, spawner, memoryExtractionModel, memoryExtractionPrompt+string(sessionBytes), workdir)
	if err != nil {
		return nil, fmt.Errorf("spawn memory extractor: %w", err)
	}
	defer func() { _ = reader.Close() }()

	var candidates []cards.CardCandidate
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if candidate := cardCandidateFromMemoryMarkerLine(scanner.Text(), 0.7); candidate != nil {
			candidates = append(candidates, *candidate)
		}
	}
	if err := scanner.Err(); err != nil {
		return candidates, fmt.Errorf("scan memory extractor output: %w", err)
	}
	return candidates, nil
}

func appendMemoryMarker(ctx context.Context, store LearningSink, beadID, line string) {
	if store == nil || beadID == "" {
		return
	}
	candidate := cardCandidateFromMemoryMarkerLine(line, 0.8)
	if candidate == nil {
		return
	}
	appendMemoryCandidates(ctx, store, beadID, []cards.CardCandidate{*candidate})
}

func cardCandidateFromMemoryMarkerLine(line string, confidence float64) *cards.CardCandidate {
	params := ParseMemoryMarker(line)
	if params == nil {
		return nil
	}
	candidate := cardCandidateFromMemoryMarker(*params, line, confidence)
	return &candidate
}

func appendMemoryCandidates(ctx context.Context, store LearningSink, beadID string, candidates []cards.CardCandidate) {
	if store == nil || beadID == "" {
		return
	}
	for _, candidate := range candidates {
		if _, err := store.AppendLearningPending(ctx, beadID, candidate); err != nil {
			log.Printf("worker memory extract: append pending learning error: %v", err)
		}
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
