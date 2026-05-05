package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"oro/pkg/codestruct"

	"github.com/spf13/cobra"
)

// newImpactCmd creates the "oro impact" subcommand.
func newImpactCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "impact <file>:<symbol>",
		Short: "Show call-graph blast radius of a symbol",
		Long: `Parses all Go files in the enclosing module and prints JSON describing
the call-graph blast radius of the named symbol:

  direct_callers     — in-project callers at depth 1 (sorted, deduped)
  transitive_callers — depth-2 callers not already in direct_callers
  cross_package_callees — callees in other packages of the same module
  external_callees   — callees outside the module (stdlib, vendor, etc.)

The file path must point to a Go source file inside a Go module (go.mod
must exist somewhere in its ancestor tree). The symbol is the unqualified
function or method name, optionally prefixed with the receiver type
(e.g. Dispatcher.Run or Run).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath, qualifiedSym, err := parseImpactArg(args[0])
			if err != nil {
				return err
			}

			absFile, err := filepath.Abs(filePath)
			if err != nil {
				return fmt.Errorf("impact: resolve path %s: %w", filePath, err)
			}

			projectRoot, err := codestruct.FindGoModDir(filepath.Dir(absFile))
			if err != nil {
				return fmt.Errorf("impact: %w", err)
			}

			symbolName := symbolLeaf(qualifiedSym)

			result, err := codestruct.ComputeImpact(projectRoot, absFile, symbolName)
			if err != nil {
				return fmt.Errorf("impact: %w", err)
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
}

// parseImpactArg splits "path/to/file.go:Symbol" into (filePath, symbol).
func parseImpactArg(arg string) (filePath, symbol string, err error) {
	idx := strings.IndexByte(arg, ':')
	if idx < 0 {
		return "", "", fmt.Errorf("impact: argument must be <file>:<symbol>, got %q", arg)
	}
	return arg[:idx], arg[idx+1:], nil
}

// symbolLeaf extracts the unqualified name from a potentially receiver-qualified
// symbol like "Dispatcher.Run" → "Run", or "StartAll" → "StartAll".
func symbolLeaf(qualified string) string {
	if dot := strings.LastIndexByte(qualified, '.'); dot >= 0 {
		return qualified[dot+1:]
	}
	return qualified
}
