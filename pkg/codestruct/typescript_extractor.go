//go:build cgo

package codestruct

import (
	"context"
	"fmt"
	"os"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// ExtractTSSymbols parses a TypeScript source file and returns all declared symbols.
func ExtractTSSymbols(filePath string) ([]Symbol, error) {
	src, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from trusted callers (worker prompts, CLI)
	if err != nil {
		return nil, fmt.Errorf("codestruct: read %s: %w", filePath, err)
	}
	return extractTSSymbols(src)
}

func extractTSSymbols(src []byte) ([]Symbol, error) {
	root, err := sitter.ParseCtx(context.Background(), src, typescript.GetLanguage())
	if err != nil {
		return nil, fmt.Errorf("codestruct: parse TypeScript: %w", err)
	}

	var symbols []Symbol
	for i := range root.ChildCount() {
		child := root.Child(int(i))
		if child == nil {
			continue
		}
		symbols = append(symbols, extractTSTopLevel(child, src, false)...)
	}
	return symbols, nil
}

func extractTSTopLevel(n *sitter.Node, src []byte, exported bool) []Symbol {
	switch n.Type() {
	case "export_statement":
		return extractTSExportStatement(n, src)
	case "function_declaration":
		return []Symbol{extractTSFunc(n, src, exported)}
	case "class_declaration":
		return extractTSClass(n, src, exported)
	case "interface_declaration":
		return []Symbol{extractTSInterface(n, src, exported)}
	case "type_alias_declaration":
		return []Symbol{extractTSTypeAlias(n, src, exported)}
	case "lexical_declaration":
		return extractTSLexicalDecl(n, src, exported)
	}
	return nil
}

func extractTSExportStatement(n *sitter.Node, src []byte) []Symbol {
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil {
			continue
		}
		switch child.Type() {
		case "function_declaration":
			return []Symbol{extractTSFunc(child, src, true)}
		case "class_declaration":
			return extractTSClass(child, src, true)
		case "interface_declaration":
			return []Symbol{extractTSInterface(child, src, true)}
		case "type_alias_declaration":
			return []Symbol{extractTSTypeAlias(child, src, true)}
		case "lexical_declaration":
			return extractTSLexicalDecl(child, src, true)
		}
	}
	return nil
}

func extractTSFunc(n *sitter.Node, src []byte, exported bool) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := normalizeSig(n.Content(src), "function ")
	return Symbol{
		Name:       name,
		Kind:       KindFunc,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: tsTopLevelVisibility(exported),
	}
}

func extractTSClass(n *sitter.Node, src []byte, exported bool) []Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	class := Symbol{
		Name:       name,
		Kind:       KindClass,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: tsTopLevelVisibility(exported),
	}

	syms := []Symbol{class}
	body := n.ChildByFieldName("body")
	if body == nil {
		return syms
	}
	for i := range body.ChildCount() {
		child := body.Child(int(i))
		if child == nil || child.Type() != "method_definition" {
			continue
		}
		syms = append(syms, extractTSMethod(child, src, name))
	}
	return syms
}

func extractTSMethod(n *sitter.Node, src []byte, className string) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	return Symbol{
		Name:       name,
		Kind:       KindMethod,
		Receiver:   className,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: tsMethodVisibility(n, src),
	}
}

func extractTSInterface(n *sitter.Node, src []byte, exported bool) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	return Symbol{
		Name:       name,
		Kind:       KindInterface,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: tsTopLevelVisibility(exported),
	}
}

func extractTSTypeAlias(n *sitter.Node, src []byte, exported bool) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	return Symbol{
		Name:       name,
		Kind:       KindType,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: tsTopLevelVisibility(exported),
	}
}

func extractTSLexicalDecl(n *sitter.Node, src []byte, exported bool) []Symbol {
	var syms []Symbol
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil || child.Type() != "variable_declarator" {
			continue
		}
		val := child.ChildByFieldName("value")
		if val == nil || val.Type() != "arrow_function" {
			continue
		}
		name := nodeFieldContent(child, "name", src)
		sig := firstLine(child.Content(src))
		syms = append(syms, Symbol{
			Name:       name,
			Kind:       KindFunc,
			Signature:  sig,
			LineStart:  int(child.StartPoint().Row) + 1,
			LineEnd:    int(child.EndPoint().Row) + 1,
			Visibility: tsTopLevelVisibility(exported),
		})
	}
	return syms
}

func tsMethodVisibility(n *sitter.Node, src []byte) string {
	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil || child.Type() != "accessibility_modifier" {
			continue
		}
		switch child.Content(src) {
		case "private":
			return "private"
		case "protected":
			return "unexported"
		}
	}
	return "exported"
}

func tsTopLevelVisibility(exported bool) string {
	if exported {
		return "exported"
	}
	return "unexported"
}
