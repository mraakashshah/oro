package codestruct

// SymbolKind classifies a symbol's syntactic role.
type SymbolKind string

// Symbol kind constants matching the values in §6.4.
const (
	KindFunc      SymbolKind = "func"
	KindMethod    SymbolKind = "method"
	KindClass     SymbolKind = "class"
	KindType      SymbolKind = "type"
	KindInterface SymbolKind = "interface"
	KindConst     SymbolKind = "const"
	KindVar       SymbolKind = "var"
)

// Symbol is a canonical record for a named declaration extracted from source.
type Symbol struct {
	Name       string
	Kind       SymbolKind
	Receiver   string   // non-empty for methods
	Signature  string   // first line of the declaration, normalized
	LineStart  int      // 1-indexed
	LineEnd    int      // 1-indexed, inclusive
	Visibility string   // "exported" or "unexported"
	Decorators []string // Python/TS decorator names; empty for Go
}
