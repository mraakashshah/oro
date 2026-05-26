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

	"oro/pkg/protocol"
)

const (
	memoryExtractionModel = "sonnet"
	memoryExtractTimeout  = 30 * time.Second
	maxMemorySessionBytes = 50_000
)

const memoryExtractionPrompt = `Extract durable learnings from this worker session.

Return only lines in this exact format:
[MEMORY] type=<lesson|decision|gotcha|pattern|preference|summary> tags=<comma-separated-tags>: <content>

If the session contains no genuine learnings, output nothing.

Session log:
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
func ExtractMemoriesWithLLMInWorkdir(_ context.Context, spawner MemoryExtractSpawner, sessionText, beadID string, store MemoryInserter, workdir string) error {
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
		params := ParseMemoryMarker(scanner.Text())
		if params == nil {
			continue
		}
		params.BeadID = beadID
		params.Source = "llm_extracted"
		params.Confidence = 0.7
		if _, err := store.Insert(extractCtx, *params); err != nil {
			log.Printf("worker memory extract: insert error: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("worker memory extract: scan error: %v", err)
	}
	return nil
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
