//go:build cgo && darwin

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	memoryeval "oro/ad_hoc/memory_eval"
)

type runConfig struct {
	db           *sql.DB
	dbPath       string
	outPath      string
	anchorsPath  string
	cachePath    string
	seed         int64
	noAPI        bool
	now          func() time.Time
	paraphraseFn func(anchors []CorpusAnchor, cachePath string, useAPI bool) (map[int64][]string, float64, error)
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	home, _ := os.UserHomeDir()
	defaultDB := filepath.Join(home, ".oro", "projects", "oro", "state.db")
	defaultOut := filepath.Join("ad_hoc", "memory_eval", "corpus.jsonl")
	defaultAnchors := filepath.Join("ad_hoc", "memory_eval", "corpus_anchors.jsonl")
	defaultCache := filepath.Join("ad_hoc", "memory_eval", "paraphrase_cache.jsonl")

	dbPath := flag.String("db", defaultDB, "path to state.db")
	outPath := flag.String("out", defaultOut, "output corpus.jsonl path")
	anchorsPath := flag.String("anchors", defaultAnchors, "output corpus_anchors.jsonl path")
	cachePath := flag.String("cache", defaultCache, "path to paraphrase_cache.jsonl")
	seed := flag.Int64("seed", 42, "random seed for anchor selection and distractor ordering")
	noAPI := flag.Bool("no-api", false, "abort on cache miss instead of calling Haiku API")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	return run(runConfig{
		db:           db,
		dbPath:       *dbPath,
		outPath:      *outPath,
		anchorsPath:  *anchorsPath,
		cachePath:    *cachePath,
		seed:         *seed,
		noAPI:        *noAPI,
		now:          time.Now,
		paraphraseFn: ParaphraseAnchors,
	})
}

func run(cfg runConfig) int {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	paraphraseFn := cfg.paraphraseFn
	if paraphraseFn == nil {
		paraphraseFn = ParaphraseAnchors
	}

	anchors, err := SelectAnchors(cfg.db, cfg.seed, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "select anchors: %v\n", err)
		return 1
	}

	queries, fallbackRate, err := paraphraseFn(anchors, cfg.cachePath, !cfg.noAPI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "paraphrase: %v\n", err)
		if strings.Contains(err.Error(), "hard abort") {
			return 3
		}
		return 1
	}

	entries := buildCorpus(anchors, queries, cfg.seed)

	if err := writeCorpus(cfg.outPath, entries, cfg.seed, fallbackRate, cfg.now); err != nil {
		fmt.Fprintf(os.Stderr, "write corpus: %v\n", err)
		return 1
	}

	if err := WriteCorpusAnchors(cfg.anchorsPath, anchors); err != nil {
		fmt.Fprintf(os.Stderr, "write anchors: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "fallback_paraphrase_rate: %.4f\n", fallbackRate)
	if fallbackRate > 0.20 {
		fmt.Fprintf(os.Stderr, "fallback_rate %.4f > 0.20 — corpus quality insufficient; repair cache and re-run\n", fallbackRate)
		return 2
	}

	return 0
}

func boolPtr(b bool) *bool { return &b }

// selectDistractors returns up to count anchors with a different type than
// anchor, ordered by (id * 2654435761 + seed) % (1<<31).
func selectDistractors(anchor CorpusAnchor, all []CorpusAnchor, seed int64, count int) []CorpusAnchor {
	candidates := make([]CorpusAnchor, 0, len(all))
	for _, a := range all {
		if a.Type != anchor.Type {
			candidates = append(candidates, a)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		hi := (candidates[i].ID*2654435761 + seed) % (1 << 31)
		hj := (candidates[j].ID*2654435761 + seed) % (1 << 31)
		return hi < hj
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}
	return candidates
}

// buildCorpus produces 1 anchor entry + 4 distractor entries per query,
// for each anchor's 3 paraphrase queries. Total: 50×3×5 = 750 entries.
func buildCorpus(anchors []CorpusAnchor, queries map[int64][]string, seed int64) []memoryeval.CorpusEntry {
	entries := make([]memoryeval.CorpusEntry, 0, 750)
	for _, anchor := range anchors {
		qs := queries[anchor.ID]
		distractors := selectDistractors(anchor, anchors, seed, 4)
		for _, q := range qs {
			entries = append(entries, memoryeval.CorpusEntry{
				Query:             q,
				CandidateMemoryID: anchor.ID,
				Relevant:          boolPtr(true),
				Source:            "anchor",
			})
			for _, d := range distractors {
				entries = append(entries, memoryeval.CorpusEntry{
					Query:             q,
					CandidateMemoryID: d.ID,
					Relevant:          boolPtr(false),
					Source:            "distractor",
				})
			}
		}
	}
	return entries
}

// writeCorpus writes the corpus header and entries to path with LF line endings.
func writeCorpus(
	path string,
	entries []memoryeval.CorpusEntry,
	seed int64,
	fallbackRate float64,
	now func() time.Time,
) error {
	var buf bytes.Buffer
	buf.WriteString("# APPROVED\n")
	buf.WriteString("# generated: " + now().UTC().Format(time.RFC3339) + "\n")
	fmt.Fprintf(&buf, "# seed: %d\n", seed)
	fmt.Fprintf(&buf, "# fallback_rate: %.4f\n", fallbackRate)

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("encode entry: %w", err)
		}
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil { //nolint:gosec // output path is an explicit CLI destination
		return fmt.Errorf("write corpus %s: %w", path, err)
	}
	return nil
}
