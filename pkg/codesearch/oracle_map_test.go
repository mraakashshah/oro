package codesearch_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"oro/pkg/codesearch"
)

func TestFilterOracleChunksForWorktree(t *testing.T) {
	parent := t.TempDir()
	worktree := filepath.Join(parent, "worktree")
	for _, path := range []string{
		filepath.Join(worktree, "pkg", "first.go"),
		filepath.Join(worktree, "pkg", "second.go"),
		filepath.Join(parent, "sibling.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(worktree, "pkg", "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "sibling.go"), filepath.Join(worktree, "pkg", "escape.go")); err != nil {
		t.Fatal(err)
	}

	first := codesearch.ChunkRef{FilePath: "pkg/first.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "First"}
	second := codesearch.ChunkRef{FilePath: "pkg/second.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "Second"}
	got := codesearch.FilterOracleChunksForWorktree(worktree, []codesearch.ChunkRef{
		first,
		{FilePath: "pkg/missing.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "Missing"},
		{FilePath: "pkg/directory", StartLine: 1, EndLine: 1, Kind: "func", Name: "Directory"},
		{FilePath: filepath.Join(worktree, "pkg", "first.go"), StartLine: 1, EndLine: 1, Kind: "func", Name: "Absolute"},
		{FilePath: "pkg/../pkg/first.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "Traversal"},
		{FilePath: "../sibling.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "Sibling"},
		{FilePath: "pkg/escape.go", StartLine: 1, EndLine: 1, Kind: "func", Name: "Escape"},
		second,
	})
	if want := []codesearch.ChunkRef{first, second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterOracleChunksForWorktree() = %#v, want %#v", got, want)
	}

	for _, root := range []string{"", filepath.Join(parent, "missing-worktree")} {
		if got := codesearch.FilterOracleChunksForWorktree(root, []codesearch.ChunkRef{first}); got != nil {
			t.Errorf("FilterOracleChunksForWorktree(%q) = %#v, want nil", root, got)
		}
	}
}

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
