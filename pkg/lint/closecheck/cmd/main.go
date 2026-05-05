// Command closecheck reports direct Store.Close calls that should be
// Dispatcher.CloseBead calls instead.
//
// Usage: closecheck <dir> [<dir>...]
//
// Exits 0 if no violations found, 1 if violations found, 2 on error.
package main

import (
	"fmt"
	"os"

	"oro/pkg/lint/closecheck"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: closecheck <dir> [<dir>...]\n")
		os.Exit(2)
	}

	dirs := os.Args[1:]
	all := make([]closecheck.Finding, 0, len(dirs))
	for _, dir := range dirs {
		findings, err := closecheck.CheckDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "closecheck: %v\n", err)
			os.Exit(2)
		}
		all = append(all, findings...)
	}

	for _, f := range all {
		fmt.Printf("%s:%d: %s\n", f.File, f.Line, f.Text)
	}

	if len(all) > 0 {
		os.Exit(1)
	}
}
