package codesearch_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"oro/pkg/codesearch"
)

func TestOracleSearchMapPrimitives(t *testing.T) {
	t.Run("normalizes a bounded query", func(t *testing.T) {
		got := codesearch.BuildOracleQuery("  Title\u00a0", "\nDescription\twith  whitespace ", "  accepts\nall ")
		want := "Title Description with whitespace accepts all"
		if got != want {
			t.Fatalf("BuildOracleQuery() = %q, want %q", got, want)
		}

		long := strings.Repeat("界", 300)
		got = codesearch.BuildOracleQuery(long, "", "")
		if len(got) > 512 {
			t.Fatalf("query is %d bytes, want at most 512", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("query is not valid UTF-8: %q", got)
		}
		if want := strings.Repeat("界", 170); got != want {
			t.Fatalf("rune-boundary query = %q, want %q", got, want)
		}

		if got := codesearch.BuildOracleQuery(" \t", "\n", "\u00a0"); got != "" {
			t.Fatalf("empty query = %q, want empty", got)
		}
	})

	t.Run("renders bounded body-free ranked entries", func(t *testing.T) {
		chunks := []codesearch.ChunkRef{
			{FilePath: "pkg/a.go", StartLine: 3, EndLine: 9, Kind: "func", Name: "Alpha"},
			{FilePath: "pkg/a.go", StartLine: 3, EndLine: 9, Kind: "func", Name: "Alpha"},
			{FilePath: "pkg/b.go", StartLine: 1, EndLine: 1, Kind: "type", Name: "Beta"},
			{FilePath: "bad.go", StartLine: 4, EndLine: 3, Kind: "func", Name: "BadRange"},
			{FilePath: "", StartLine: 1, EndLine: 1, Kind: "func", Name: "MissingPath"},
		}

		got := codesearch.FormatOracleMap(chunks, 1024)
		want := "pkg/a.go:3-9 func Alpha\npkg/b.go:1-1 type Beta"
		if got != want {
			t.Fatalf("FormatOracleMap() = %q, want %q", got, want)
		}
		for _, absent := range []string{"BadRange", "MissingPath", "Content", "func Alpha()"} {
			if strings.Contains(got, absent) {
				t.Errorf("map unexpectedly contains %q: %q", absent, got)
			}
		}

		for _, maxBytes := range []int{0, 1, 8, 16, 32, len(got) - 1, len(got), len(got) + 1} {
			out := codesearch.FormatOracleMap(chunks, maxBytes)
			if len(out) > maxBytes {
				t.Errorf("maxBytes=%d produced %d bytes: %q", maxBytes, len(out), out)
			}
		}
		if got := codesearch.FormatOracleMap(chunks, 1); got != "" {
			t.Fatalf("budget below first entry = %q, want empty", got)
		}
	})

	t.Run("caps oversized maps at the Oracle context limit", func(t *testing.T) {
		chunks := make([]codesearch.ChunkRef, 0, 512)
		for i := range 512 {
			chunks = append(chunks, codesearch.ChunkRef{
				FilePath:  "pkg/very/long/path/to/package/file.go",
				StartLine: i + 1,
				EndLine:   i + 1,
				Kind:      "function",
				Name:      "UniqueChunkNameForOracleSearchMap",
			})
		}

		out := codesearch.FormatOracleMap(chunks, codesearch.OracleSearchContextLimit+1024)
		if len(out) > codesearch.OracleSearchContextLimit {
			t.Fatalf("oversized map is %d bytes, want at most %d", len(out), codesearch.OracleSearchContextLimit)
		}
		if !strings.Contains(out, "UniqueChunkNameForOracleSearchMap") {
			t.Fatalf("oversized map omitted ranked entries: %q", out)
		}
	})
}
