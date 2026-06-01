package codesearch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/codesearch"
)

type routeTestEmbedder struct{}

func (routeTestEmbedder) Embed(text string) []float32 {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "auth"):
		return []float32{1, 0}
	case strings.Contains(lower, "payment"):
		return []float32{0, 1}
	default:
		return []float32{0.2, 0.2}
	}
}

func (routeTestEmbedder) Dim() int {
	return 2
}

func (routeTestEmbedder) Name() string {
	return "route-test"
}

func TestRouteGrep_StructuralReturnsAST(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"Go func declaration", `func\s+\w+`},
		{"Go type struct", `type\s+Server\s+struct`},
		{"Python class", `class\s+\w+`},
		{"TS function", `function\s+\w+`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := codesearch.RouteGrep(tt.pattern)
			if result.Route != codesearch.RouteAST {
				t.Errorf("RouteGrep(%q).Route = %v, want RouteAST", tt.pattern, result.Route)
			}
			if result.Original != tt.pattern {
				t.Errorf("RouteGrep(%q).Original = %q, want %q", tt.pattern, result.Original, tt.pattern)
			}
		})
	}
}

func TestRouteGrep_LiteralPassthrough(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"TODO marker", "TODO"},
		{"simple identifier", "handleRequest"},
		{"error string", "connection refused"},
		{"bare keyword", "func"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := codesearch.RouteGrep(tt.pattern)
			if result.Route != codesearch.RouteRipgrep {
				t.Errorf("RouteGrep(%q).Route = %v, want RouteRipgrep", tt.pattern, result.Route)
			}
			if result.Original != tt.pattern {
				t.Errorf("RouteGrep(%q).Original = %q, want %q", tt.pattern, result.Original, tt.pattern)
			}
		})
	}
}

func TestRouteGrep_SemanticFlagged(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"natural language", "where is auth logic"},
		{"how question", "how does authentication work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := codesearch.RouteGrep(tt.pattern)
			if result.Route != codesearch.RouteSemantic {
				t.Errorf("RouteGrep(%q).Route = %v, want RouteSemantic", tt.pattern, result.Route)
			}
		})
	}
}

func TestSearchSemantic_NoLongerDeadEnds(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "code-index.db")

	src := `package sample

func AuthenticateUser() error {
	return nil
}

func ProcessPayment() error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write sample source: %v", err)
	}

	idx, err := codesearch.NewCodeIndex(dbPath)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()
	idx.SetEmbedder(routeTestEmbedder{})

	if _, err := idx.Build(ctx, root); err != nil {
		t.Fatalf("Build: %v", err)
	}

	route := codesearch.RouteGrep("where is auth logic")
	if route.Route != codesearch.RouteSemantic {
		t.Fatalf("RouteGrep route = %v, want semantic", route.Route)
	}

	results, err := idx.SearchSemantic(ctx, route.Original)
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchSemantic returned no results")
	}
	if got := results[0].Chunk.Name; got != "AuthenticateUser" {
		t.Fatalf("top result = %q, want AuthenticateUser", got)
	}
}

func TestSearchSemantic_FailsOpenToFTS5WhenEmbedderUnavailable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "code-index.db")

	src := `package sample

func AuthenticateUser() error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write sample source: %v", err)
	}

	idx, err := codesearch.NewCodeIndex(dbPath)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	if _, err := idx.Build(ctx, root); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.SearchSemantic(ctx, "AuthenticateUser")
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchSemantic returned no FTS5 fallback results")
	}
	if got := results[0].Chunk.Name; got != "AuthenticateUser" {
		t.Fatalf("top fallback result = %q, want AuthenticateUser", got)
	}
}

func TestRouteGrep_PreservesOriginalPattern(t *testing.T) {
	patterns := []string{
		`func\s+\w+`,
		"handleRequest",
		"where is auth logic",
		"",
	}

	for _, p := range patterns {
		result := codesearch.RouteGrep(p)
		if result.Original != p {
			t.Errorf("RouteGrep(%q).Original = %q, want original preserved", p, result.Original)
		}
	}
}

func TestGrepRouteResult_String(t *testing.T) {
	tests := []struct {
		route codesearch.GrepRoute
		want  string
	}{
		{codesearch.RouteRipgrep, "ripgrep"},
		{codesearch.RouteAST, "ast"},
		{codesearch.RouteSemantic, "semantic"},
	}

	for _, tt := range tests {
		got := tt.route.String()
		if got != tt.want {
			t.Errorf("GrepRoute(%d).String() = %q, want %q", tt.route, got, tt.want)
		}
	}
}
