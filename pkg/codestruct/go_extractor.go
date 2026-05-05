//go:build cgo

// Package codestruct provides tree-sitter-backed symbol extraction for Go source files.
package codestruct

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
)

// ExtractGoSymbols parses a Go source file and returns all declared symbols.
func ExtractGoSymbols(filePath string) ([]Symbol, error) {
	src, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from trusted callers (worker prompts, CLI)
	if err != nil {
		return nil, fmt.Errorf("codestruct: read %s: %w", filePath, err)
	}
	return extractGoSymbols(src)
}

func extractGoSymbols(src []byte) ([]Symbol, error) {
	root, err := sitter.ParseCtx(context.Background(), src, golang.GetLanguage())
	if err != nil {
		return nil, fmt.Errorf("codestruct: parse Go: %w", err)
	}

	var symbols []Symbol
	for i := range root.ChildCount() {
		child := root.Child(int(i))
		if child == nil {
			continue
		}
		symbols = append(symbols, extractGoTopLevel(child, src)...)
	}
	return symbols, nil
}

func extractGoTopLevel(n *sitter.Node, src []byte) []Symbol {
	switch n.Type() {
	case "function_declaration":
		return []Symbol{extractGoFunc(n, src)}
	case "method_declaration":
		return []Symbol{extractGoMethod(n, src)}
	case "type_declaration":
		return extractGoTypeDecl(n, src)
	case "const_declaration":
		return extractGoConstOrVar(n, src, "const_spec", KindConst)
	case "var_declaration":
		return extractGoConstOrVar(n, src, "var_spec", KindVar)
	}
	return nil
}

func extractGoFunc(n *sitter.Node, src []byte) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := normalizeSig(n.Content(src), "func ")
	return Symbol{
		Name:       name,
		Kind:       KindFunc,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: goVisibility(name),
	}
}

func extractGoMethod(n *sitter.Node, src []byte) Symbol {
	name := nodeFieldContent(n, "name", src)
	receiver := extractReceiverType(n, src)
	sig := normalizeSig(n.Content(src), "func ")
	return Symbol{
		Name:       name,
		Kind:       KindMethod,
		Receiver:   receiver,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: goVisibility(name),
	}
}

func extractReceiverType(method *sitter.Node, src []byte) string {
	receiverList := method.ChildByFieldName("receiver")
	if receiverList == nil {
		return ""
	}
	for i := range receiverList.ChildCount() {
		child := receiverList.Child(int(i))
		if child == nil || child.Type() != "parameter_declaration" {
			continue
		}
		typeNode := child.ChildByFieldName("type")
		if typeNode != nil {
			return typeNode.Content(src)
		}
	}
	return ""
}

func extractGoTypeDecl(n *sitter.Node, src []byte) []Symbol {
	var syms []Symbol
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil || child.Type() != "type_spec" {
			continue
		}
		name := nodeFieldContent(child, "name", src)
		if name == "" {
			continue
		}
		kind := KindType
		typeNode := child.ChildByFieldName("type")
		if typeNode != nil && typeNode.Type() == "interface_type" {
			kind = KindInterface
		}
		sig := firstLine(child.Content(src))
		syms = append(syms, Symbol{
			Name:       name,
			Kind:       kind,
			Signature:  sig,
			LineStart:  int(child.StartPoint().Row) + 1,
			LineEnd:    int(child.EndPoint().Row) + 1,
			Visibility: goVisibility(name),
		})
	}
	return syms
}

func extractGoConstOrVar(n *sitter.Node, src []byte, specType string, kind SymbolKind) []Symbol {
	var syms []Symbol
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil || child.Type() != specType {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		names := extractIdentList(nameNode, src)
		sig := firstLine(child.Content(src))
		for _, name := range names {
			syms = append(syms, Symbol{
				Name:       name,
				Kind:       kind,
				Signature:  sig,
				LineStart:  int(child.StartPoint().Row) + 1,
				LineEnd:    int(child.EndPoint().Row) + 1,
				Visibility: goVisibility(name),
			})
		}
	}
	return syms
}

// extractIdentList returns names from an identifier or identifier_list node.
func extractIdentList(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	if n.Type() == "identifier" {
		return []string{n.Content(src)}
	}
	var names []string
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child != nil && child.Type() == "identifier" {
			names = append(names, child.Content(src))
		}
	}
	return names
}

// nodeFieldContent returns the source text of a named field child, or "".
//
//nolint:unparam // field is intentionally generic; callers happen to all use "name" today but the helper is shared across extractors
func nodeFieldContent(n *sitter.Node, field string, src []byte) string {
	child := n.ChildByFieldName(field)
	if child == nil {
		return ""
	}
	return child.Content(src)
}

// normalizeSig takes the first line of src, strips the leading keyword prefix,
// and removes a trailing " {".
func normalizeSig(nodeSrc, stripPrefix string) string {
	line := firstLine(nodeSrc)
	line = strings.TrimPrefix(line, stripPrefix)
	line = strings.TrimSuffix(strings.TrimSpace(line), "{")
	return strings.TrimSpace(line)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func goVisibility(name string) string {
	if name == "" {
		return "unexported"
	}
	r, _ := utf8.DecodeRuneInString(name)
	if unicode.IsUpper(r) {
		return "exported"
	}
	return "unexported"
}
