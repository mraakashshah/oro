package codestruct

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

// ExtractPySymbols parses a Python source file and returns all declared symbols.
func ExtractPySymbols(filePath string) ([]Symbol, error) {
	src, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from trusted callers (worker prompts, CLI)
	if err != nil {
		return nil, fmt.Errorf("codestruct: read %s: %w", filePath, err)
	}
	return extractPySymbols(src)
}

func extractPySymbols(src []byte) ([]Symbol, error) {
	root, err := sitter.ParseCtx(context.Background(), src, python.GetLanguage())
	if err != nil {
		return nil, fmt.Errorf("codestruct: parse Python: %w", err)
	}

	var symbols []Symbol
	for i := range root.ChildCount() {
		child := root.Child(int(i))
		if child == nil {
			continue
		}
		symbols = append(symbols, extractPyTopLevel(child, src, "")...)
	}
	return symbols, nil
}

func extractPyTopLevel(n *sitter.Node, src []byte, receiver string) []Symbol {
	switch n.Type() {
	case "function_definition", "async_function_definition":
		return []Symbol{extractPyFunc(n, src, receiver)}
	case "class_definition":
		return extractPyClass(n, src)
	case "decorated_definition":
		return extractPyDecorated(n, src, receiver)
	}
	return nil
}

func extractPyFunc(n *sitter.Node, src []byte, receiver string) Symbol {
	name := nodeFieldContent(n, "name", src)
	kind := KindFunc
	if receiver != "" {
		kind = KindMethod
	}
	sig := firstLine(n.Content(src))
	return Symbol{
		Name:       name,
		Kind:       kind,
		Receiver:   receiver,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: pyVisibility(name),
	}
}

func extractPyClass(n *sitter.Node, src []byte) []Symbol {
	name := nodeFieldContent(n, "name", src)
	sig := firstLine(n.Content(src))
	class := Symbol{
		Name:       name,
		Kind:       KindClass,
		Signature:  sig,
		LineStart:  int(n.StartPoint().Row) + 1,
		LineEnd:    int(n.EndPoint().Row) + 1,
		Visibility: pyVisibility(name),
	}

	syms := []Symbol{class}
	body := n.ChildByFieldName("body")
	if body == nil {
		return syms
	}
	for i := range body.ChildCount() {
		child := body.Child(int(i))
		if child == nil {
			continue
		}
		syms = append(syms, extractPyTopLevel(child, src, name)...)
	}
	return syms
}

func extractPyDecorated(n *sitter.Node, src []byte, receiver string) []Symbol {
	var decorators []string
	var inner *sitter.Node

	for i := range n.ChildCount() {
		child := n.Child(int(i))
		if child == nil {
			continue
		}
		switch child.Type() {
		case "decorator":
			decorators = append(decorators, strings.TrimPrefix(firstLine(child.Content(src)), "@"))
		case "function_definition", "async_function_definition":
			inner = child
		case "class_definition":
			inner = child
		}
	}

	if inner == nil {
		return nil
	}

	switch inner.Type() {
	case "function_definition", "async_function_definition":
		sym := extractPyFunc(inner, src, receiver)
		sym.Decorators = decorators
		return []Symbol{sym}
	case "class_definition":
		syms := extractPyClass(inner, src)
		if len(syms) > 0 {
			syms[0].Decorators = decorators
		}
		return syms
	}
	return nil
}

func pyVisibility(name string) string {
	r, _ := utf8.DecodeRuneInString(name)
	if r == '_' {
		return "unexported"
	}
	return "exported"
}
