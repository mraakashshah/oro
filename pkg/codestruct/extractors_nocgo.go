//go:build !cgo

// Package codestruct extracts symbols and call graph information from source code.
package codestruct

import "errors"

// errNoCGO is returned by all extraction functions when CGO is disabled.
// Tree-sitter requires CGO; build with CGO_ENABLED=1 to enable symbol extraction.
var errNoCGO = errors.New("codestruct: CGO is required for tree-sitter symbol extraction")

// ExtractGoSymbols is a no-op stub; CGO is required.
func ExtractGoSymbols(_ string) ([]Symbol, error) { return nil, errNoCGO }

// ExtractPySymbols is a no-op stub; CGO is required.
func ExtractPySymbols(_ string) ([]Symbol, error) { return nil, errNoCGO }

// ExtractJSSymbols is a no-op stub; CGO is required.
func ExtractJSSymbols(_ string) ([]Symbol, error) { return nil, errNoCGO }

// ExtractTSSymbols is a no-op stub; CGO is required.
func ExtractTSSymbols(_ string) ([]Symbol, error) { return nil, errNoCGO }

// BuildCallGraph is a no-op stub; CGO is required.
func BuildCallGraph(_ []string, _ map[string][]Symbol) ([]CallEdge, []string, error) {
	return nil, nil, errNoCGO
}
