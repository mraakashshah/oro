//go:build cgo

package codestruct

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// BuildCallGraph walks call expressions in the given Go files and resolves callees
// to symbols in pkgSymbols. files are the Go source files to analyse (one package).
// pkgSymbols maps every in-project file path to its extracted symbols; the caller
// populates this with symbols from all packages that might be called.
// Returns resolved edges, warning strings for each unresolved callee, and any parse error.
func BuildCallGraph(files []string, pkgSymbols map[string][]Symbol) ([]CallEdge, []string, error) {
	// allSymsByName maps symbol name → first file where it appears (for simple resolution).
	allSymsByName := make(map[string]string, len(pkgSymbols)*8)
	for fp, syms := range pkgSymbols {
		for _, sym := range syms {
			if _, exists := allSymsByName[sym.Name]; !exists {
				allSymsByName[sym.Name] = fp
			}
		}
	}

	// pkgDirFiles maps the last directory segment of a file → file paths.
	// Used to match import aliases (which are typically the last path segment) to files.
	pkgDirFiles := make(map[string][]string, len(pkgSymbols))
	for fp := range pkgSymbols {
		dir := filepath.Base(filepath.Dir(fp))
		pkgDirFiles[dir] = append(pkgDirFiles[dir], fp)
	}

	var allEdges []CallEdge
	var allWarnings []string

	for _, fp := range files {
		src, err := os.ReadFile(fp) //nolint:gosec // fp comes from trusted callers
		if err != nil {
			return nil, nil, fmt.Errorf("codestruct: read %s: %w", fp, err)
		}
		root, err := sitter.ParseCtx(context.Background(), src, golang.GetLanguage())
		if err != nil {
			return nil, nil, fmt.Errorf("codestruct: parse %s: %w", fp, err)
		}

		imports := extractGoImportsMap(root, src)
		w := &goCallWalker{
			src:           src,
			filePath:      fp,
			imports:       imports,
			pkgDirFiles:   pkgDirFiles,
			pkgSymbols:    pkgSymbols,
			allSymsByName: allSymsByName,
		}
		w.walk(root, "")

		allEdges = append(allEdges, w.edges...)
		allWarnings = append(allWarnings, w.warnings...)
	}

	return allEdges, allWarnings, nil
}

// goCallWalker walks a parsed Go AST collecting call edges.
type goCallWalker struct {
	src           []byte
	filePath      string
	imports       map[string]string   // import alias → last segment of import path
	pkgDirFiles   map[string][]string // last dir segment → []file paths
	pkgSymbols    map[string][]Symbol
	allSymsByName map[string]string // symbolName → filePath
	edges         []CallEdge
	warnings      []string
}

func (w *goCallWalker) walk(n *sitter.Node, enclosing string) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "function_declaration", "method_declaration":
		name := nodeFieldContent(n, "name", w.src)
		for i := range n.ChildCount() {
			w.walk(n.Child(int(i)), name)
		}
		return // children already walked with updated enclosing

	case "call_expression":
		w.processCall(n, enclosing)
		// fall through: walk children to catch nested calls in arguments
	}

	for i := range n.ChildCount() {
		w.walk(n.Child(int(i)), enclosing)
	}
}

func (w *goCallWalker) processCall(n *sitter.Node, enclosing string) {
	fnNode := n.ChildByFieldName("function")
	if fnNode == nil {
		return
	}

	line := int(n.StartPoint().Row) + 1

	switch fnNode.Type() {
	case "identifier":
		callee := fnNode.Content(w.src)
		w.resolveSimple(callee, callee, enclosing, line)

	case "selector_expression":
		operand := fnNode.ChildByFieldName("operand")
		field := fnNode.ChildByFieldName("field")
		if operand == nil || field == nil {
			return
		}
		selector := operand.Content(w.src)
		funcName := field.Content(w.src)
		rawCallee := selector + "." + funcName

		if lastSeg, ok := w.imports[selector]; ok {
			// Import alias: cross-package call.
			w.resolvePkg(lastSeg, funcName, rawCallee, enclosing, line)
		} else {
			// Method call: resolve by name only (receiver-type disambiguation deferred to Layer 3).
			w.resolveSimple(funcName, rawCallee, enclosing, line)
		}
	}
}

// resolveSimple looks up calleeSym across all pkgSymbols by name.
func (w *goCallWalker) resolveSimple(calleeSym, rawCallee, enclosing string, line int) {
	if fp, ok := w.allSymsByName[calleeSym]; ok {
		w.edges = append(w.edges, CallEdge{
			CallerFile:   w.filePath,
			CallerSymbol: enclosing,
			CalleeName:   rawCallee,
			CalleeFile:   fp,
			CalleeSymbol: calleeSym,
			Line:         line,
			Resolved:     true,
		})
		return
	}
	slog.Debug("unresolved callee", "file", w.filePath, "callee", rawCallee, "line", line)
	w.warnings = append(w.warnings, fmt.Sprintf("%s:%d: unresolved %q", w.filePath, line, rawCallee))
	w.edges = append(w.edges, CallEdge{
		CallerFile:   w.filePath,
		CallerSymbol: enclosing,
		CalleeName:   rawCallee,
		Line:         line,
		Resolved:     false,
	})
}

// resolvePkg resolves a cross-package callee by matching the import alias to
// files in pkgSymbols whose directory's last segment equals pkgLastSeg.
func (w *goCallWalker) resolvePkg(pkgLastSeg, funcName, rawCallee, enclosing string, line int) {
	if fps, ok := w.pkgDirFiles[pkgLastSeg]; ok {
		for _, fp := range fps {
			for _, sym := range w.pkgSymbols[fp] {
				if sym.Name == funcName {
					w.edges = append(w.edges, CallEdge{
						CallerFile:   w.filePath,
						CallerSymbol: enclosing,
						CalleeName:   rawCallee,
						CalleeFile:   fp,
						CalleeSymbol: funcName,
						Line:         line,
						Resolved:     true,
					})
					return
				}
			}
		}
	}
	slog.Debug("unresolved cross-pkg callee", "file", w.filePath, "callee", rawCallee, "line", line)
	w.warnings = append(w.warnings, fmt.Sprintf("%s:%d: unresolved %q", w.filePath, line, rawCallee))
	w.edges = append(w.edges, CallEdge{
		CallerFile:   w.filePath,
		CallerSymbol: enclosing,
		CalleeName:   rawCallee,
		Line:         line,
		Resolved:     false,
	})
}

// extractGoImportsMap parses import declarations from a Go AST root and returns
// a map of alias → last path segment.
func extractGoImportsMap(root *sitter.Node, src []byte) map[string]string {
	result := make(map[string]string)
	for i := range root.ChildCount() {
		decl := root.Child(int(i))
		if decl == nil || decl.Type() != "import_declaration" {
			continue
		}
		collectImportAliases(decl, src, result)
	}
	return result
}

// collectImportAliases walks one import_declaration node and populates out.
func collectImportAliases(decl *sitter.Node, src []byte, out map[string]string) {
	for j := range decl.ChildCount() {
		child := decl.Child(int(j))
		if child == nil {
			continue
		}
		switch child.Type() {
		case "import_spec":
			addImportAlias(child, src, out)
		case "import_spec_list":
			collectImportSpecList(child, src, out)
		}
	}
}

// collectImportSpecList walks an import_spec_list node.
func collectImportSpecList(list *sitter.Node, src []byte, out map[string]string) {
	for k := range list.ChildCount() {
		spec := list.Child(int(k))
		if spec != nil && spec.Type() == "import_spec" {
			addImportAlias(spec, src, out)
		}
	}
}

// addImportAlias adds one import_spec's alias → lastSeg entry to out.
func addImportAlias(spec *sitter.Node, src []byte, out map[string]string) {
	pathNode := spec.ChildByFieldName("path")
	if pathNode == nil {
		return
	}
	raw := strings.Trim(pathNode.Content(src), `"`)
	parts := strings.Split(raw, "/")
	lastSeg := parts[len(parts)-1]

	nameNode := spec.ChildByFieldName("name")
	if nameNode != nil {
		alias := nameNode.Content(src)
		if alias != "_" && alias != "." {
			out[alias] = lastSeg
			return
		}
	}
	out[lastSeg] = lastSeg
}
