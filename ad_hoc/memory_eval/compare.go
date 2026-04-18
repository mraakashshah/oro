//go:build ignore

// compare.go — precision@k eval CLI for the memory retrieval pipeline.
//
// Usage:
//
//	go run ad_hoc/memory_eval/compare.go \
//	  --corpus=ad_hoc/memory_eval/corpus.jsonl \
//	  --baseline=tfidf --target=dispatcher-warm --k=10
//
// Exit codes:
//
//	0  gate passed (dispatcher-warm ≥ 1.30× baseline AND solo-cli-cold ≥ 1.20× baseline)
//	1  gate failed (thresholds not met; table printed to stdout)
//	2  config or search error
//	3  missing "# APPROVED" marker in corpus file
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	eval "oro/ad_hoc/memory_eval"
)

func main() {
	corpusPath := flag.String("corpus", "", "path to corpus JSONL file (required)")
	baseline := flag.String("baseline", "tfidf", "baseline config; only \"tfidf\" is supported")
	target := flag.String("target", "dispatcher-warm", "target config to evaluate")
	k := flag.Int("k", 10, "top-k cutoff for precision (> 0)")
	flag.Parse()

	// Validate flags.
	if *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "error: --corpus is required")
		os.Exit(2)
	}
	if *baseline != "tfidf" {
		fmt.Fprintf(os.Stderr, "error: --baseline must be \"tfidf\" (got %q)\n", *baseline)
		os.Exit(2)
	}
	validTargets := map[string]bool{"tfidf": true, "dispatcher-warm": true, "solo-cli-cold": true}
	if !validTargets[*target] {
		fmt.Fprintf(os.Stderr, "error: --target must be one of: tfidf, dispatcher-warm, solo-cli-cold (got %q)\n", *target)
		os.Exit(2)
	}
	if *k <= 0 {
		fmt.Fprintf(os.Stderr, "error: --k must be > 0 (got %d)\n", *k)
		os.Exit(2)
	}

	// Require human approval marker before running eval.
	ok, err := eval.HasApprovalMarker(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not check approval marker: %v\n", err)
		os.Exit(3)
	}
	if !ok {
		fmt.Fprintf(os.Stderr,
			"error: corpus %q is missing the approval marker.\n"+
				"Add a line \"# APPROVED\" to the file after human review, then re-run.\n",
			*corpusPath)
		os.Exit(3)
	}

	// Load corpus.
	entries, err := eval.LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load corpus: %v\n", err)
		os.Exit(2)
	}
	if len(entries) < 100 {
		fmt.Fprintf(os.Stderr, "warning: corpus has only %d entries (< 100); results may be unreliable\n", len(entries))
	}

	// Run all three configurations.
	type result struct {
		name string
		p5   float64
		p10  float64
	}
	cfgs := []string{"tfidf", "dispatcher-warm", "solo-cli-cold"}
	results := make([]result, 0, len(cfgs))
	for _, cfg := range cfgs {
		p5, p10, runErr := eval.RunConfig(entries, cfg, *k)
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: config %q failed: %v\n", cfg, runErr)
			os.Exit(2)
		}
		results = append(results, result{cfg, p5, p10})
	}

	// Locate baseline precision.
	var baseP10 float64
	for _, r := range results {
		if r.name == "tfidf" {
			baseP10 = r.p10
			break
		}
	}

	// Print per-config table.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "config\tprec@5\tprec@10\tratio_vs_baseline")
	for _, r := range results {
		ratio := 0.0
		if baseP10 > 0 {
			ratio = r.p10 / baseP10
		}
		fmt.Fprintf(w, "%s\t%.4f\t%.4f\t%.4f\n", r.name, r.p5, r.p10, ratio)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "error: flush table: %v\n", err)
	}

	// Gate check.
	var warmP10, coldP10 float64
	for _, r := range results {
		switch r.name {
		case "dispatcher-warm":
			warmP10 = r.p10
		case "solo-cli-cold":
			coldP10 = r.p10
		}
	}

	if !eval.CheckGate(baseP10, warmP10, coldP10) {
		fmt.Fprintf(os.Stderr,
			"\ngate FAILED: dispatcher-warm needs ≥ %.0f%% of baseline, solo-cli-cold needs ≥ %.0f%%\n",
			warmThreshold*100, coldThreshold*100)
		os.Exit(1)
	}
	fmt.Printf("\ngate PASSED: precision thresholds met (warm=%.4f ≥ %.2f×base, cold=%.4f ≥ %.2f×base)\n",
		warmP10, warmThreshold, coldP10, coldThreshold)
}

const (
	warmThreshold = 1.30
	coldThreshold = 1.20
)
