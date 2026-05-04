package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"oro/pkg/codestruct"

	"github.com/spf13/cobra"
)

func newOutlineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outline <file>",
		Short: "Print a symbol outline for a Go source file",
		Long:  "Parses a Go source file with tree-sitter and prints all declared symbols with their kind, visibility, and line range.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if filepath.Ext(path) != ".go" {
				return fmt.Errorf("outline: only .go files are supported")
			}
			symbols, err := codestruct.ExtractGoSymbols(path)
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
