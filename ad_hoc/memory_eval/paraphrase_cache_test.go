//nolint:testpackage // tests stay in-package for cache helpers
package memoryeval

import (
	"os"
	"strings"
	"testing"
)

func TestParaphraseCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/paraphrase_cache.jsonl"

	// Write 3 entries with keys intentionally in non-sorted order.
	entries := map[string]CacheEntry{
		CacheKey("cccccccccccccccc", "v1"): {AnchorSHA: "cccccccccccccccc", PromptVersion: "v1", Queries: []string{"c query 1", "c query 2", "c query 3"}},
		CacheKey("aaaaaaaaaaaaaaaa", "v1"): {AnchorSHA: "aaaaaaaaaaaaaaaa", PromptVersion: "v1", Queries: []string{"a query 1", "a query 2", "a query 3"}},
		CacheKey("bbbbbbbbbbbbbbbb", "v1"): {AnchorSHA: "bbbbbbbbbbbbbbbb", PromptVersion: "v1", Queries: []string{"b query 1", "b query 2", "b query 3"}},
	}

	if err := WriteCache(path, entries); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}

	// Read back and assert equal.
	got, err := ReadCache(path)
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for k, want := range entries {
		g, ok := got[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if g.AnchorSHA != want.AnchorSHA {
			t.Errorf("key %q: AnchorSHA %q, want %q", k, g.AnchorSHA, want.AnchorSHA)
		}
		if g.PromptVersion != want.PromptVersion {
			t.Errorf("key %q: PromptVersion %q, want %q", k, g.PromptVersion, want.PromptVersion)
		}
		if len(g.Queries) != len(want.Queries) {
			t.Errorf("key %q: len(Queries) %d, want %d", k, len(g.Queries), len(want.Queries))
			continue
		}
		for i, q := range want.Queries {
			if g.Queries[i] != q {
				t.Errorf("key %q: Queries[%d] %q, want %q", k, i, g.Queries[i], q)
			}
		}
	}

	// Assert file has keys sorted lexicographically and no CRLF.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "\r\n") {
		t.Error("file contains CRLF line endings, want LF only")
	}

	aIdx := strings.Index(content, "aaaaaaaaaaaaaaaa")
	bIdx := strings.Index(content, "bbbbbbbbbbbbbbbb")
	cIdx := strings.Index(content, "cccccccccccccccc")
	if aIdx < 0 || bIdx < 0 || cIdx < 0 {
		t.Fatalf("one or more anchor SHAs missing from file")
	}
	if aIdx >= bIdx || bIdx >= cIdx {
		t.Errorf("keys not sorted lexicographically in file: a=%d b=%d c=%d", aIdx, bIdx, cIdx)
	}
}

func TestParaphraseCacheMissingFile(t *testing.T) {
	got, err := ReadCache("/nonexistent/path/that/does/not/exist/cache.jsonl")
	if err != nil {
		t.Fatalf("ReadCache on missing file must return empty map, not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %d entries", len(got))
	}
}

func TestParaphraseCacheMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/paraphrase_cache.jsonl"
	content := `{"anchor_sha":"aaa","prompt_version":"v1","queries":["q1","q2","q3"]}` + "\n" +
		`not-valid-json` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := ReadCache(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON line, got nil")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error should mention line number 2, got: %v", err)
	}
}

func TestCacheKey(t *testing.T) {
	got := CacheKey("a1b2c3d4e5f67890", "v1")
	want := "a1b2c3d4e5f67890/v1"
	if got != want {
		t.Errorf("CacheKey = %q, want %q", got, want)
	}
}
