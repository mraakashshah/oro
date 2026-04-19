// ad_hoc/memory_eval/paraphrase_cache.go
// JSONL read/write for the paraphrase query cache.
package memoryeval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ParaphrasePromptVersion is the current prompt version. Bumping it invalidates
// all cache entries (every key embeds the version).
const ParaphrasePromptVersion = "v1"

// CacheEntry holds cached paraphrase queries for one anchor.
type CacheEntry struct {
	AnchorSHA     string   `json:"anchor_sha"`
	PromptVersion string   `json:"prompt_version"`
	Queries       []string `json:"queries"`
}

// CacheKey returns the map key for a given anchor SHA and prompt version.
func CacheKey(anchorSHA, promptVersion string) string {
	return anchorSHA + "/" + promptVersion
}

// ReadCache reads a JSONL paraphrase cache file and returns a map keyed by
// CacheKey(entry.AnchorSHA, entry.PromptVersion). A missing file returns an
// empty map without error. A malformed JSON line returns an error that includes
// the 1-based line number.
func ReadCache(path string) (map[string]CacheEntry, error) {
	f, err := os.Open(path) //nolint:gosec // caller-controlled path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]CacheEntry{}, nil
		}
		return nil, fmt.Errorf("open cache: %w", err)
	}
	defer f.Close()

	result := make(map[string]CacheEntry)
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry CacheEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("paraphrase_cache line %d: %w", lineNum, err)
		}
		result[CacheKey(entry.AnchorSHA, entry.PromptVersion)] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	return result, nil
}

// WriteCache serializes entries to path as JSONL with keys sorted
// lexicographically and LF line endings.
func WriteCache(path string, entries map[string]CacheEntry) error {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		line, err := json.Marshal(entries[k])
		if err != nil {
			return fmt.Errorf("marshal cache entry %q: %w", k, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return os.WriteFile(path, buf.Bytes(), 0o644) //nolint:gosec // caller-controlled path
}
