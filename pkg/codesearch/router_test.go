package codesearch_test

import (
	"testing"

	"oro/pkg/codesearch"
)

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
