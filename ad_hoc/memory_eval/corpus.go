// ad_hoc/memory_eval/corpus.go
// LoadCorpus + CorpusEntry for the memory retrieval eval corpus.
package memoryeval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// CorpusEntry is one candidate evaluation pair.
type CorpusEntry struct {
	Query             string `json:"query"`
	CandidateMemoryID int64  `json:"candidate_memory_id"`
	Relevant          *bool  `json:"relevant"`
	Source            string `json:"source"`
}

// CorpusAnchor is one seeded memory from corpus_anchors.jsonl.
type CorpusAnchor struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// LoadCorpusAnchors reads a JSONL anchor sidecar file, skipping blank lines
// and comment lines (starting with '#').
func LoadCorpusAnchors(path string) ([]CorpusAnchor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open anchors: %w", err)
	}
	defer func() { _ = f.Close() }()

	var anchors []CorpusAnchor
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var a CorpusAnchor
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			return nil, fmt.Errorf("parse anchor line %q: %w", line, err)
		}
		anchors = append(anchors, a)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan anchors: %w", err)
	}
	return anchors, nil
}

// LoadCorpus reads a JSONL corpus file, skipping blank lines and lines starting
// with '#' (used for header comments such as "# source: history").
func LoadCorpus(path string) ([]CorpusEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open corpus: %w", err)
	}
	defer func() { _ = f.Close() }()

	var entries []CorpusEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var e CorpusEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse corpus line %q: %w", line, err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan corpus: %w", err)
	}
	return entries, nil
}
