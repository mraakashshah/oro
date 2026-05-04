package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"oro/pkg/codestruct"

	"github.com/spf13/cobra"
)

func outlineExtract(path string) ([]codestruct.Symbol, error) {
	var (
		syms []codestruct.Symbol
		err  error
	)
	switch filepath.Ext(path) {
	case ".go":
		syms, err = codestruct.ExtractGoSymbols(path)
	case ".py", ".pyi":
		syms, err = codestruct.ExtractPySymbols(path)
	case ".ts", ".tsx":
		syms, err = codestruct.ExtractTSSymbols(path)
	case ".js", ".mjs", ".cjs", ".jsx":
		syms, err = codestruct.ExtractJSSymbols(path)
	default:
		return nil, fmt.Errorf("unsupported file extension: %s", filepath.Ext(path))
	}
	if err != nil {
		return nil, fmt.Errorf("extract symbols: %w", err)
	}
	return syms, nil
}

func newOutlineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outline <file>",
		Short: "Print a symbol outline for a source file",
		Long:  "Parses a source file with tree-sitter and prints all declared symbols with their kind, visibility, and line range. Supports Go, Python, TypeScript, and JavaScript.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			symbols, err := outlineExtract(path)
			if err != nil {
				return fmt.Errorf("outline: %w", err)
			}
			if len(symbols) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no symbols found)")
				return nil
			}
			var b strings.Builder
			for _, s := range symbols {
				recv := ""
				if s.Receiver != "" {
					recv = fmt.Sprintf("(%s).", s.Receiver)
				}
				fmt.Fprintf(&b, "L%-4d %-10s %s%s [%s]\n",
					s.LineStart, string(s.Kind), recv, s.Name, s.Visibility)
			}
			fmt.Fprint(cmd.OutOrStdout(), b.String())
			return nil
		},
	}
}
