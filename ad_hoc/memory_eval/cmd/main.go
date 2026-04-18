// CLI for extracting the memory eval corpus.
// Usage: go run ./ad_hoc/memory_eval/cmd/ [--db <state.db>] [--out <corpus.jsonl>]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	memoryeval "oro/ad_hoc/memory_eval"
)

func main() {
	home, _ := os.UserHomeDir()
	defaultDB := filepath.Join(home, ".oro", "state.db")
	defaultOut := filepath.Join("ad_hoc", "memory_eval", "corpus.jsonl")

	dbPath := flag.String("db", defaultDB, "path to state.db")
	outPath := flag.String("out", defaultOut, "output corpus.jsonl path")
	flag.Parse()

	if err := memoryeval.ExtractCorpus(*dbPath, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "extract: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", *outPath)
}
