//go:build cgo && darwin

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	memoryeval "oro/ad_hoc/memory_eval"
)

// paraphraseSystem is the standard system prompt for the Haiku paraphrase request.
const paraphraseSystem = `Generate exactly 3 paraphrase queries for the given memory note.
Each query should be a natural question using different vocabulary than the original.
Respond with ONLY a JSON array of 3 strings, no other text.
Example: ["question one", "question two", "question three"]`

// paraphraseSystemStrict is used on the second attempt after an overlap violation.
const paraphraseSystemStrict = `Generate exactly 3 paraphrase queries for the given memory note.
Use vocabulary that is COMPLETELY different from the original — avoid repeating its words.
Each query must be phrased as a natural question. Respond ONLY with a JSON array of 3 strings.`

// claudeCallerFn is the injectable function used to call the Haiku model.
// system is the system prompt; content is the anchor text to paraphrase.
// Returns exactly 3 query strings on success.
type claudeCallerFn func(system, content string) ([]string, error)

// anchorSHA returns the first 8 bytes of sha256(content) as 16 lowercase hex chars.
func anchorSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// extractVerbPhrase drops the first word of anchor and lower-cases the remainder.
// Used to build templated fallback queries: "how do I <verbPhrase> in this system".
func extractVerbPhrase(anchor string) string {
	words := strings.Fields(anchor)
	if len(words) <= 1 {
		return strings.ToLower(anchor)
	}
	return strings.ToLower(strings.Join(words[1:], " "))
}

// validateQueries returns true if every query shares ≤ MaxSharedContentWords
// content words with anchorContent.
func validateQueries(queries []string, anchorContent string) bool {
	for _, q := range queries {
		if memoryeval.CountSharedContentWords(q, anchorContent) > memoryeval.MaxSharedContentWords {
			return false
		}
	}
	return true
}

// generateQueries produces 3 paraphrase queries for anchor with retry/fallback.
//
// Retry policy:
//   - JSON error on first call → retry once with same prompt; second error → abort run.
//   - Overlap violation on first result → re-prompt once with strict instructions.
//   - Second overlap violation → templated fallback via extractVerbPhrase.
//   - Templated fallback also violates overlap → hard abort (no silent bad data).
func generateQueries(anchor CorpusAnchor, caller claudeCallerFn) (queries []string, wasFallback bool, err error) {
	sha := anchorSHA(anchor.Content)

	// Phase 1: standard prompt with one JSON-error retry.
	queries, err = caller(paraphraseSystem, anchor.Content)
	if err != nil {
		queries, err = caller(paraphraseSystem, anchor.Content)
		if err != nil {
			return nil, false, fmt.Errorf("anchor id=%d sha=%s: haiku failed twice: %w", anchor.ID, sha, err)
		}
	}

	// Phase 2: if overlap passes, done.
	if validateQueries(queries, anchor.Content) {
		return queries, false, nil
	}

	// Phase 3: re-prompt once with strict instructions.
	strictQueries, strictErr := caller(paraphraseSystemStrict, anchor.Content)
	if strictErr == nil && validateQueries(strictQueries, anchor.Content) {
		return strictQueries, false, nil
	}

	// Phase 4: templated fallback.
	vp := extractVerbPhrase(anchor.Content)
	tq := "how do I " + vp + " in this system"
	if memoryeval.CountSharedContentWords(tq, anchor.Content) > memoryeval.MaxSharedContentWords {
		return nil, false, fmt.Errorf("anchor id=%d content=%q: templated fallback violates overlap constraint — hard abort",
			anchor.ID, anchor.Content)
	}
	return []string{tq, tq, tq}, true, nil
}

// paraphraseAnchorsWithCaller is the injectable core for ParaphraseAnchors.
// Reads the cache, processes each anchor in order, and writes the cache atomically
// after each new result (preserving partial progress on failure).
// If useAPI is false and any anchor is absent from the cache, the full anchor list is
// scanned and a single error listing all missing SHAs is returned.
func paraphraseAnchorsWithCaller(
	anchors []CorpusAnchor,
	cachePath string,
	useAPI bool,
	caller claudeCallerFn,
) (queriesByAnchor map[int64][]string, fallbackRate float64, err error) {
	cache, err := memoryeval.ReadCache(cachePath)
	if err != nil {
		return nil, 0, fmt.Errorf("read cache: %w", err)
	}

	result := make(map[int64][]string, len(anchors))
	var missingFromAPI []string
	fallbackCount := 0

	for _, anchor := range anchors {
		sha := anchorSHA(anchor.Content)
		key := memoryeval.CacheKey(sha, memoryeval.ParaphrasePromptVersion)

		if entry, ok := cache[key]; ok {
			result[anchor.ID] = entry.Queries
			continue
		}

		if !useAPI {
			missingFromAPI = append(missingFromAPI, sha)
			continue
		}

		queries, wasFallback, genErr := generateQueries(anchor, caller)
		if genErr != nil {
			return nil, 0, genErr
		}
		if wasFallback {
			fallbackCount++
		}

		result[anchor.ID] = queries
		cache[key] = memoryeval.CacheEntry{
			AnchorSHA:     sha,
			PromptVersion: memoryeval.ParaphrasePromptVersion,
			Queries:       queries,
		}
		if writeErr := memoryeval.WriteCache(cachePath, cache); writeErr != nil {
			return nil, 0, fmt.Errorf("write cache after anchor %d: %w", anchor.ID, writeErr)
		}
	}

	if len(missingFromAPI) > 0 {
		return nil, 0, fmt.Errorf("cache miss for anchors (--no-api mode): %s",
			strings.Join(missingFromAPI, ", "))
	}

	fallbackRate = 0.0
	if len(anchors) > 0 {
		fallbackRate = float64(fallbackCount) / float64(len(anchors))
	}
	return result, fallbackRate, nil
}

// claudeEnvelope is the JSON wrapper returned by `claude -p --output-format json`.
type claudeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// callClaude shells out to the claude CLI to generate 3 paraphrase queries.
func callClaude(system, content string) ([]string, error) {
	//nolint:gosec // args are program-controlled, not user-supplied
	cmd := exec.CommandContext(context.Background(), "claude", "-p",
		"--model", "claude-haiku-4-5-20251001",
		"--output-format", "json",
		"--system", system,
		content,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude CLI: %w", err)
	}
	var env claudeEnvelope
	if jsonErr := json.Unmarshal(out, &env); jsonErr != nil {
		return nil, fmt.Errorf("parse claude envelope: %w", jsonErr)
	}
	if env.IsError {
		return nil, fmt.Errorf("claude returned error: %s", env.Result)
	}
	var queries []string
	if jsonErr := json.Unmarshal([]byte(env.Result), &queries); jsonErr != nil {
		return nil, fmt.Errorf("parse queries from claude result: %w", jsonErr)
	}
	if len(queries) != 3 {
		return nil, fmt.Errorf("expected 3 queries from claude, got %d", len(queries))
	}
	return queries, nil
}

// ParaphraseAnchors generates paraphrase queries for each anchor, reading from
// cache first and calling the Haiku model on misses when useAPI is true.
// Returns queries per anchor ID, the fraction that used templated fallback, and any error.
func ParaphraseAnchors(anchors []CorpusAnchor, cachePath string, useAPI bool) (
	queriesByAnchor map[int64][]string,
	fallbackRate float64,
	err error,
) {
	return paraphraseAnchorsWithCaller(anchors, cachePath, useAPI, callClaude)
}
