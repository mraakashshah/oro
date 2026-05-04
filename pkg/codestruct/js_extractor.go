package codestruct

import (
	"context"
	"fmt"
	"os"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
)

// ExtractJSSymbols parses a JavaScript source file and returns all declared symbols.
func ExtractJSSymbols(filePath string) ([]Symbol, error) {
	src, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from trusted callers (worker prompts, CLI)
	if err != nil {
		return nil, fmt.Errorf("codestruct: read %s: %w", filePath, err)
	}
	return extractJSSymbols(src)
}

func extractJSSymbols(src []byte) ([]Symbol, error) {
	root, err := sitter.ParseCtx(context.Background(), src, javascript.GetLanguage())
	if err != nil {
		return nil, fmt.Errorf("codestruct: parse JavaScript: %w", err)
	}

	var symbols []Symbol
	for i := range root.ChildCount() {
		child := root.Child(int(i))
		if child == nil {
			continue
		}
		symbols = append(symbols, extractJSTopLevel(child, src)...)
	}
	return symbols, nil
}

func extractJSTopLevel(n *sitter.Node, src []byte) []Symbol {
	switch n.Type() {
	case "function_declaration":
		return []Symbol{extractJSFunc(n, src)}
	case "class_declaration":
		return extractJSClass(n, src)
	case "lexical_declaration":
		return extractJSLexicalDecl(n, src)
	}
	return nil
}

func extractJSFunc(n *sitter.Node, src []byte) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := normalizeSig(n.Content(src), "function ")
	return Symbol{
		Name:       name,
		Kind:       KindFunc,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: "exported",
	}
}

func extractJSClass(n *sitter.Node, src []byte) []Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	class := Symbol{
		Name:       name,
		Kind:       KindClass,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: "exported",
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
		syms = append(syms, extractJSMethod(child, src, name))
	}
	return syms
}

func extractJSMethod(n *sitter.Node, src []byte, className string) Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	return Symbol{
		Name:       name,
		Kind:       KindMethod,
		Receiver:   className,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: "exported",
	}
}

func extractJSLexicalDecl(n *sitter.Node, src []byte) []Symbol {
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
			Visibility: "exported",
		})
	}
	return syms
}
