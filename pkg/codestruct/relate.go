package codestruct

import (
	"path/filepath"
	"strings"
)

// ResolveCallee returns a canonical file-qualified symbol reference for a call
// edge when it can be resolved without falling back to global bare-name lookup.
//
//oro:testonly — production wiring deferred to Phase 1 SymbolHints/card relation feed.
func ResolveCallee(e CallEdge, importsByFile map[string]map[string]string, symsByFile map[string][]Symbol) (ref string, ok bool) {
	symName := edgeSymbolName(e)
	if e.Resolved && e.CalleeFile != "" && symName != "" {
		return canonicalSymbolRef(e.CalleeFile, symName), true
	}

	if qualifier, name, qualified := strings.Cut(e.CalleeName, "."); qualified {
		return resolveImportedCallee(e.CallerFile, qualifier, name, importsByFile, symsByFile)
	}

	return resolveSameFileCallee(e.CallerFile, symName, symsByFile)
}

func edgeSymbolName(e CallEdge) string {
	if e.CalleeSymbol != "" {
		return e.CalleeSymbol
	}
	if _, name, ok := strings.Cut(e.CalleeName, "."); ok {
		return name
	}
	return e.CalleeName
}

func resolveSameFileCallee(callerFile, name string, symsByFile map[string][]Symbol) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, sym := range symsByFile[callerFile] {
		if sym.Name == name {
			return canonicalSymbolRef(callerFile, name), true
		}
	}
	return "", false
}

func resolveImportedCallee(
	callerFile string,
	qualifier string,
	name string,
	importsByFile map[string]map[string]string,
	symsByFile map[string][]Symbol,
) (string, bool) {
	if name == "" {
		return "", false
	}
	importTarget, ok := importsByFile[callerFile][qualifier]
	if !ok || importTarget == "" {
		return "", false
	}

	var match string
	for file, syms := range symsByFile {
		if !fileMatchesImport(file, importTarget) {
			continue
		}
		for _, sym := range syms {
			if sym.Name != name {
				continue
			}
			if match != "" {
				return "", false
			}
			match = canonicalSymbolRef(file, name)
		}
	}
	return match, match != ""
}

func fileMatchesImport(file, importTarget string) bool {
	dir := filepath.ToSlash(filepath.Dir(file))
	target := strings.Trim(importTarget, "/")
	if target == "" {
		return false
	}
	return filepath.Base(dir) == filepath.Base(target) || strings.HasSuffix(dir, "/"+target)
}

func canonicalSymbolRef(file, symbol string) string {
	return filepath.ToSlash(file) + ":" + symbol
}
