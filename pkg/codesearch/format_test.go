package codesearch_test

import (
	"strings"
	"testing"

	"oro/pkg/codesearch"
)

func TestFormatResults_Empty(t *testing.T) {
	if got := codesearch.FormatResults(nil); got != "" {
		t.Errorf("FormatResults(nil) = %q, want empty", got)
	}
	if got := codesearch.FormatResults([]codesearch.SearchResult{}); got != "" {
		t.Errorf("FormatResults([]) = %q, want empty", got)
	}
}

func TestFormatResults_SingleResult(t *testing.T) {
	results := []codesearch.SearchResult{
		{
			Chunk: codesearch.Chunk{
				FilePath:  "pkg/example/foo.go",
				StartLine: 10,
				EndLine:   20,
				Content:   "func Foo() {}",
			},
			Score: 0.9,
		},
	}

	got := codesearch.FormatResults(results)

	if !strings.Contains(got, "### pkg/example/foo.go:10-20") {
		t.Errorf("expected file path header, got %q", got)
	}
	if !strings.Contains(got, "func Foo() {}") {
		t.Errorf("expected code content, got %q", got)
	}
	if !strings.Contains(got, "```") {
		t.Errorf("expected code block markers, got %q", got)
	}
}

func TestFormatResults_WithReason(t *testing.T) {
	results := []codesearch.SearchResult{
		{
			Chunk: codesearch.Chunk{
				FilePath:  "pkg/a.go",
				StartLine: 1,
				EndLine:   5,
				Content:   "package a",
			},
			Reason: "Direct match for search query",
		},
	}

	got := codesearch.FormatResults(results)

	if !strings.Contains(got, "_Relevance: Direct match for search query_") {
		t.Errorf("expected relevance note, got %q", got)
	}
}

func TestFormatResults_SkipsEmptyContent(t *testing.T) {
	results := []codesearch.SearchResult{
		{
			Chunk: codesearch.Chunk{
				FilePath:  "pkg/empty.go",
				StartLine: 1,
				EndLine:   1,
				Content:   "",
			},
		},
		{
			Chunk: codesearch.Chunk{
				FilePath:  "pkg/real.go",
				StartLine: 5,
				EndLine:   10,
				Content:   "func Real() {}",
			},
		},
	}

	got := codesearch.FormatResults(results)

	if strings.Contains(got, "empty.go") {
		t.Errorf("expected empty content result to be skipped, got %q", got)
	}
	if !strings.Contains(got, "real.go") {
		t.Errorf("expected non-empty result to be included, got %q", got)
	}
}

func TestFormatResults_MultipleResults(t *testing.T) {
	results := []codesearch.SearchResult{
		{
			Chunk: codesearch.Chunk{
				FilePath:  "a.go",
				StartLine: 1,
				EndLine:   3,
				Content:   "func A() {}",
			},
		},
		{
			Chunk: codesearch.Chunk{
				FilePath:  "b.go",
				StartLine: 10,
				EndLine:   15,
				Content:   "func B() {}",
			},
			Reason: "Helper function",
		},
	}

	got := codesearch.FormatResults(results)

	if !strings.Contains(got, "a.go:1-3") {
		t.Errorf("expected first result header, got %q", got)
	}
	if !strings.Contains(got, "b.go:10-15") {
		t.Errorf("expected second result header, got %q", got)
	}
}
