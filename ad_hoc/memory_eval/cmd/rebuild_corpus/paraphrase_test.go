//go:build cgo && darwin

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	memoryeval "oro/ad_hoc/memory_eval"
)

// testSHA mirrors anchorSHA: sha256(content)[:8] as 16 lowercase hex chars.
func testSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}

// TestParaphraseLoopCacheHit verifies that anchors with cached entries use the
// cached queries without invoking the claude CLI.
func TestParaphraseLoopCacheHit(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	anchors := []CorpusAnchor{
		{ID: 10, Type: "lesson", Content: "workers recover from crashes automatically"},
		{ID: 20, Type: "gotcha", Content: "dispatch queue fills up under load"},
	}
	wantQueries := []string{
		"how do processes restart after failure",
		"what is the respawn mechanism",
		"describe the recovery procedure",
	}

	cacheEntries := make(map[string]memoryeval.CacheEntry)
	for _, a := range anchors {
		sha := testSHA(a.Content)
		key := memoryeval.CacheKey(sha, memoryeval.ParaphrasePromptVersion)
		cacheEntries[key] = memoryeval.CacheEntry{
			AnchorSHA:     sha,
			PromptVersion: memoryeval.ParaphrasePromptVersion,
			Queries:       wantQueries,
		}
	}
	if err := memoryeval.WriteCache(cachePath, cacheEntries); err != nil {
		t.Fatalf("setup cache: %v", err)
	}

	callerInvoked := false
	noCaller := func(_, _ string) ([]string, error) {
		callerInvoked = true
		return nil, fmt.Errorf("unexpected claude call on cache hit")
	}

	result, fallbackRate, err := paraphraseAnchorsWithCaller(anchors, cachePath, true, noCaller)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callerInvoked {
		t.Error("claude caller was invoked despite full cache hit")
	}
	if fallbackRate != 0.0 {
		t.Errorf("fallback rate: got %v, want 0.0", fallbackRate)
	}
	if len(result) != 2 {
		t.Fatalf("result length: got %d, want 2", len(result))
	}
	for _, a := range anchors {
		got, ok := result[a.ID]
		if !ok {
			t.Errorf("anchor id=%d missing from result", a.ID)
			continue
		}
		for i, q := range wantQueries {
			if i >= len(got) {
				t.Errorf("anchor id=%d: got %d queries, want %d", a.ID, len(got), len(wantQueries))
				break
			}
			if got[i] != q {
				t.Errorf("anchor id=%d query[%d]: got %q, want %q", a.ID, i, got[i], q)
			}
		}
	}
}

// TestParaphraseLoopNoAPIAbort verifies that when useAPI is false, cache misses
// return an error that lists all missing anchor SHAs.
func TestParaphraseLoopNoAPIAbort(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl") // no file written

	anchors := []CorpusAnchor{
		{ID: 1, Type: "lesson", Content: "workers recover from crashes automatically"},
		{ID: 2, Type: "gotcha", Content: "dispatch queue fills up under load"},
	}

	callerInvoked := false
	noCaller := func(_, _ string) ([]string, error) {
		callerInvoked = true
		return nil, fmt.Errorf("unexpected claude call in --no-api mode")
	}

	_, _, err := paraphraseAnchorsWithCaller(anchors, cachePath, false, noCaller)
	if err == nil {
		t.Fatal("want error on cache miss with useAPI=false, got nil")
	}
	if callerInvoked {
		t.Error("claude caller was invoked despite useAPI=false")
	}
	for _, a := range anchors {
		sha := testSHA(a.Content)
		if !strings.Contains(err.Error(), sha) {
			t.Errorf("error %q missing SHA for anchor id=%d: want substring %q", err.Error(), a.ID, sha)
		}
	}
}

// TestParaphraseLoopAPICalledOnMiss verifies the caller is invoked on a cache miss
// when useAPI=true, and the result is stored.
func TestParaphraseLoopAPICalledOnMiss(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	anchor := CorpusAnchor{ID: 5, Type: "lesson", Content: "workers recover from crashes automatically"}
	validQueries := []string{
		"how do processes restart after failure",
		"what is the respawn mechanism",
		"describe the recovery procedure",
	}

	callerCalled := false
	fakeCaller := func(_, _ string) ([]string, error) {
		callerCalled = true
		return validQueries, nil
	}

	result, fallbackRate, err := paraphraseAnchorsWithCaller(
		[]CorpusAnchor{anchor}, cachePath, true, fakeCaller,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !callerCalled {
		t.Error("caller not invoked on cache miss with useAPI=true")
	}
	if fallbackRate != 0.0 {
		t.Errorf("fallback rate: got %v, want 0.0", fallbackRate)
	}
	got, ok := result[anchor.ID]
	if !ok {
		t.Fatalf("anchor id=%d missing from result", anchor.ID)
	}
	for i, q := range validQueries {
		if i >= len(got) || got[i] != q {
			t.Errorf("query[%d]: got %v, want %q", i, got, q)
			break
		}
	}
}

// TestParaphraseLoopJSONErrorRetry verifies that a single caller error triggers a
// retry and the second successful call's queries are used.
func TestParaphraseLoopJSONErrorRetry(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	anchor := CorpusAnchor{ID: 1, Content: "workers recover from crashes automatically"}
	goodQueries := []string{
		"how do processes restart after failure",
		"what is the respawn mechanism",
		"describe the recovery procedure",
	}

	calls := 0
	fakeCaller := func(_, _ string) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("simulated JSON parse error")
		}
		return goodQueries, nil
	}

	result, _, err := paraphraseAnchorsWithCaller([]CorpusAnchor{anchor}, cachePath, true, fakeCaller)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 caller invocations, got %d", calls)
	}
	got := result[anchor.ID]
	if len(got) != 3 || got[0] != goodQueries[0] {
		t.Errorf("got %v, want %v", got, goodQueries)
	}
}

// TestParaphraseLoopJSONErrorTwiceAborts verifies that two consecutive caller errors
// abort the whole run with an error.
func TestParaphraseLoopJSONErrorTwiceAborts(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	anchor := CorpusAnchor{ID: 1, Content: "workers recover from crashes automatically"}
	fakeCaller := func(_, _ string) ([]string, error) {
		return nil, fmt.Errorf("simulated JSON parse error")
	}

	_, _, err := paraphraseAnchorsWithCaller([]CorpusAnchor{anchor}, cachePath, true, fakeCaller)
	if err == nil {
		t.Fatal("want error on double JSON failure, got nil")
	}
}

// TestParaphraseLoopOverlapRetryThenValid verifies that on an overlap violation the
// caller is re-invoked with the strict system prompt, and the second valid result is used.
func TestParaphraseLoopOverlapRetryThenValid(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	// anchor content words (after lemmatization, stop-word removal):
	//   {worker, recover, crash, automatically}
	anchor := CorpusAnchor{ID: 1, Content: "workers recover from crashes automatically"}

	// violatingQueries share 4 content words with anchor: {worker,crash,recover,automatically}
	violatingQueries := []string{
		"workers crashes recover automatically restart",
		"workers crashes recover automatically restart",
		"workers crashes recover automatically restart",
	}
	validQueries := []string{
		"how do processes restart after failure",
		"what is the respawn mechanism",
		"describe the recovery procedure",
	}

	strictCalled := false
	fakeCaller := func(system, _ string) ([]string, error) {
		if system == paraphraseSystem {
			return violatingQueries, nil
		}
		strictCalled = true
		return validQueries, nil
	}

	result, fallbackRate, err := paraphraseAnchorsWithCaller(
		[]CorpusAnchor{anchor}, cachePath, true, fakeCaller,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strictCalled {
		t.Error("strict re-prompt not called on overlap violation")
	}
	if fallbackRate != 0.0 {
		t.Errorf("fallback rate: got %v, want 0.0", fallbackRate)
	}
	got := result[anchor.ID]
	if len(got) != 3 || got[0] != validQueries[0] {
		t.Errorf("result queries: got %v, want %v", got, validQueries)
	}
}

// TestParaphraseLoopTemplateFallback verifies that when both standard and strict
// prompts produce overlap violations, the templated fallback is used.
func TestParaphraseLoopTemplateFallback(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	// anchor content words: {worker, recover, crash, automatically}
	anchor := CorpusAnchor{ID: 1, Content: "workers recover from crashes automatically"}
	// violatingQueries: {worker,crash,recover,automatically,restart} — 4 shared words > 3
	violatingQueries := []string{
		"workers crashes recover automatically restart",
		"workers crashes recover automatically restart",
		"workers crashes recover automatically restart",
	}

	fakeCaller := func(_, _ string) ([]string, error) {
		return violatingQueries, nil
	}

	result, fallbackRate, err := paraphraseAnchorsWithCaller(
		[]CorpusAnchor{anchor}, cachePath, true, fakeCaller,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallbackRate <= 0.0 {
		t.Errorf("fallback rate: got %v, want > 0", fallbackRate)
	}
	got, ok := result[anchor.ID]
	if !ok || len(got) == 0 {
		t.Fatalf("anchor id=%d: no result, want templated fallback", anchor.ID)
	}
	if !strings.HasPrefix(got[0], "how do I ") {
		t.Errorf("template query %q does not start with 'how do I '", got[0])
	}
}

// TestParaphraseLoopTemplateHardAbort verifies that if the templated fallback query
// also violates the overlap constraint, the run aborts with a hard error.
func TestParaphraseLoopTemplateHardAbort(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	// anchor content words: {recover, crash, restart, system, automatically}
	// verbPhrase = "crash restart system automatically" (first word "recover" dropped)
	// template = "how do I crash restart system automatically in this system"
	// template content words: {crash, restart, system, automatically}
	// shared with anchor: 4 > MaxSharedContentWords=3 => HARD ABORT
	anchor := CorpusAnchor{ID: 1, Content: "recover crash restart system automatically"}
	// violatingQueries share 5 words with anchor
	violatingQueries := []string{
		"recover crash restart system automatically again",
		"recover crash restart system automatically again",
		"recover crash restart system automatically again",
	}

	fakeCaller := func(_, _ string) ([]string, error) {
		return violatingQueries, nil
	}

	_, _, err := paraphraseAnchorsWithCaller([]CorpusAnchor{anchor}, cachePath, true, fakeCaller)
	if err == nil {
		t.Fatal("want hard abort when template violates overlap, got nil error")
	}
}

// TestParaphraseLoopCachePartialProgress verifies that the cache is written after
// each anchor so that a partial run preserves already-computed results.
func TestParaphraseLoopCachePartialProgress(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")

	anchors := []CorpusAnchor{
		{ID: 1, Content: "workers recover from crashes automatically"},
		{ID: 2, Content: "dispatch queue fills up under load"},
	}
	validQueries := []string{
		"how do processes restart after failure",
		"what is the respawn mechanism",
		"describe the recovery procedure",
	}

	fakeCaller := func(_, content string) ([]string, error) {
		if content == anchors[0].Content {
			return validQueries, nil
		}
		return nil, fmt.Errorf("simulated failure for anchor 2")
	}

	_, _, err := paraphraseAnchorsWithCaller(anchors, cachePath, true, fakeCaller)
	if err == nil {
		t.Fatal("want error for anchor 2 failure, got nil")
	}

	// Anchor 1's result must be in the cache despite the partial failure
	cache, readErr := memoryeval.ReadCache(cachePath)
	if readErr != nil {
		t.Fatalf("read cache: %v", readErr)
	}
	sha1 := testSHA(anchors[0].Content)
	key1 := memoryeval.CacheKey(sha1, memoryeval.ParaphrasePromptVersion)
	entry, ok := cache[key1]
	if !ok {
		t.Fatalf("anchor 1 SHA %s not in cache after partial run", sha1)
	}
	if len(entry.Queries) != 3 {
		t.Errorf("anchor 1 queries in cache: got %d, want 3", len(entry.Queries))
	}
}
