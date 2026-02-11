package codesearch

// GrepRoute identifies which search backend should handle a grep query.
type GrepRoute int

const (
	// RouteRipgrep passes the pattern through to ripgrep unmodified.
	RouteRipgrep GrepRoute = iota
	// RouteAST routes the pattern to AST-based structural search.
	RouteAST
	// RouteSemantic flags the pattern as a natural-language query for future handling.
	RouteSemantic
)

// String returns a human-readable label for the grep route.
func (r GrepRoute) String() string {
	switch r {
	case RouteRipgrep:
		return "ripgrep"
	case RouteAST:
		return "ast"
	case RouteSemantic:
		return "semantic"
	default:
		return "unknown"
	}
}

// GrepRouteResult holds the routing decision for a grep query.
type GrepRouteResult struct {
	// Route is the search backend to use.
	Route GrepRoute
	// Original is the original grep pattern, preserved unmodified.
	Original string
}

// RouteGrep classifies a grep pattern and returns a routing decision.
// Pure function with no side effects.
//
// Routing logic:
//   - Structural patterns (func\s+\w+, type Server struct) -> RouteAST
//   - Semantic patterns (where is auth logic?) -> RouteSemantic
//   - Everything else (TODO, handleRequest) -> RouteRipgrep (passthrough)
func RouteGrep(pattern string) GrepRouteResult {
	queryType := ClassifyQuery(pattern)

	var route GrepRoute
	switch queryType {
	case QueryStructural:
		route = RouteAST
	case QuerySemantic:
		route = RouteSemantic
	default:
		route = RouteRipgrep
	}

	return GrepRouteResult{
		Route:    route,
		Original: pattern,
	}
}
