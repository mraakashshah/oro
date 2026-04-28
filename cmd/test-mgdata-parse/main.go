package main

import (
	"fmt"
	"io"
	"os"

	"oro/pkg/mg/data"
)

func main() {
	if err := run(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "test-mgdata-parse: %v\n", err)
		os.Exit(1)
	}
}

func run(r io.Reader) error {
	out, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if _, err := data.ParseIssuesJSON(out, ""); err != nil {
		return err
	}
	return nil
}
