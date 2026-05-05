package codestruct

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ImpactResult holds the call-graph blast radius of a single symbol.
type ImpactResult struct {
	Symbol            string   `json:"symbol"`
	File              string   `json:"file"`
	DirectCallers     []string `json:"direct_callers"`
	TransitiveCallers []string `json:"transitive_callers"`
	CrossPkgCallees   []string `json:"cross_package_callees"`
	ExternalCallees   []string `json:"external_callees"`
}

// ComputeImpact walks all Go files under projectRoot and returns the
// call-graph blast radius of the symbol named symbolName in targetFile.
// projectRoot must be the directory containing go.mod.
// targetFile must be an absolute path to a Go source file under projectRoot.
// symbolName is the unqualified function/method name (e.g. "Run").
func ComputeImpact(projectRoot, targetFile, symbolName string) (*ImpactResult, error) {
	allFiles, err := walkGoFiles(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("impact: walk files: %w", err)
	}

	pkgSymbols := make(map[string][]Symbol, len(allFiles))
	for _, f := range allFiles {
		syms, err := ExtractGoSymbols(f)
		if err != nil {
			return nil, fmt.Errorf("impact: extract symbols %s: %w", f, err)
		}
		pkgSymbols[f] = syms
	}

	edges, _, err := BuildCallGraph(allFiles, pkgSymbols)
	if err != nil {
		return nil, fmt.Errorf("impact: build call graph: %w", err)
	}

	direct, crossPkg, external := classifyEdges(edges, projectRoot, targetFile, symbolName)
	transitive := buildTransitiveSet(edges, projectRoot, direct)

	return &ImpactResult{
		Symbol:            symbolName,
		File:              mustRel(projectRoot, targetFile),
		DirectCallers:     sortedKeys(direct),
		TransitiveCallers: sortedKeys(transitive),
		CrossPkgCallees:   sortedKeys(crossPkg),
		ExternalCallees:   sortedKeys(external),
	}, nil
}

// classifyEdges partitions edges into direct callers, cross-package callees,
// and external callees relative to targetFile:symbolName.
func classifyEdges(edges []CallEdge, projectRoot, targetFile, symbolName string) (
	direct, crossPkg, external map[string]bool,
) {
	direct = make(map[string]bool)
	crossPkg = make(map[string]bool)
	external = make(map[string]bool)
	targetDir := filepath.Dir(targetFile)

	for _, e := range edges {
		if isDirectCaller(e, targetFile, symbolName) {
			if e.CallerSymbol != "" {
				direct[relativeRef(projectRoot, e.CallerFile, e.CallerSymbol)] = true
			}
			continue
		}
		if !isFromTarget(e, targetFile, symbolName) {
			continue
		}
		if e.Resolved && filepath.Dir(e.CalleeFile) != targetDir {
			crossPkg[relativePackageRef(projectRoot, e.CalleeFile, e.CalleeSymbol)] = true
		} else if !e.Resolved && strings.ContainsRune(e.CalleeName, '.') {
			external[e.CalleeName] = true
		}
	}
	return direct, crossPkg, external
}

// buildTransitiveSet returns callers of direct-caller symbols that are not
// themselves direct callers.
func buildTransitiveSet(edges []CallEdge, projectRoot string, direct map[string]bool) map[string]bool {
	transitive := make(map[string]bool)
	for _, e := range edges {
		if !e.Resolved || e.CallerSymbol == "" {
			continue
		}
		calleeRef := relativeRef(projectRoot, e.CalleeFile, e.CalleeSymbol)
		if !direct[calleeRef] {
			continue
		}
		callerRef := relativeRef(projectRoot, e.CallerFile, e.CallerSymbol)
		if !direct[callerRef] {
			transitive[callerRef] = true
		}
	}
	return transitive
}

func isDirectCaller(e CallEdge, targetFile, symbolName string) bool {
	return e.Resolved && e.CalleeFile == targetFile && e.CalleeSymbol == symbolName
}

func isFromTarget(e CallEdge, targetFile, symbolName string) bool {
	return e.CallerFile == targetFile && e.CallerSymbol == symbolName
}

// FindGoModDir walks up from startDir until it finds a directory containing
// go.mod and returns that directory.
func FindGoModDir(startDir string) (string, error) {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("impact: no go.mod found above %s", startDir)
		}
		dir = parent
	}
}

// walkGoFiles returns all .go files under dir, sorted.
func walkGoFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walkGoFiles %s: %w", dir, err)
	}
	sort.Strings(files)
	return files, nil
}

// relativeRef returns "rel/path/to/file.go:SymbolName" relative to root.
func relativeRef(root, absFile, sym string) string {
	return mustRel(root, absFile) + ":" + sym
}

// relativePackageRef returns "rel/pkg/path.SymbolName" relative to root,
// using the package directory as the package qualifier.
func relativePackageRef(root, absFile, sym string) string {
	pkgPath := mustRel(root, filepath.Dir(absFile))
	return pkgPath + "." + sym
}

func mustRel(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return rel
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
