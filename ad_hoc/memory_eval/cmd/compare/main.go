//go:build cgo && darwin

// compare — MRR/hit-rate eval CLI for the memory retrieval pipeline.
//
// Usage:
//
//	go run -tags 'cgo darwin' ad_hoc/memory_eval/cmd/compare/main.go \
//	  --corpus=ad_hoc/memory_eval/corpus.jsonl \
//	  --anchors=ad_hoc/memory_eval/corpus_anchors.jsonl
//
// Exit codes:
//
//	0  gate passed AND eval_report.yaml written
//	1  gate failed AND eval_report.yaml written
//	2  config or I/O error
//	3  missing "# APPROVED" marker in corpus file
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	eval "oro/ad_hoc/memory_eval"
	"oro/pkg/memory"

	"gopkg.in/yaml.v3"
)

// EvalReport is the YAML schema written to eval_report.yaml.
type EvalReport struct {
	Timestamp string                   `yaml:"timestamp"`
	Hardware  HardwareInfo             `yaml:"hardware"`
	Corpus    CorpusInfo               `yaml:"corpus"`
	Models    ModelsInfo               `yaml:"models"`
	Configs   map[string]ConfigMetrics `yaml:"configs"`
	Gate      GateInfo                 `yaml:"gate"`
}

// HardwareInfo records the machine where the eval ran.
type HardwareInfo struct {
	Arch string `yaml:"arch"`
	OS   string `yaml:"os"`
	CPU  string `yaml:"cpu"`
}

// CorpusInfo records corpus provenance and sizes.
type CorpusInfo struct {
	Seed                   int     `yaml:"seed"`
	CorpusSHA              string  `yaml:"corpus_sha"`
	AnchorsSHA             string  `yaml:"anchors_sha"`
	ParaphraseCacheSHA     string  `yaml:"paraphrase_cache_sha"`
	InputsSHA              string  `yaml:"inputs_sha"`
	NumQueriesScored       int     `yaml:"num_queries_scored"`
	NumAnchors             int     `yaml:"num_anchors"`
	NumPairs               int     `yaml:"num_pairs"`
	FallbackParaphraseRate float64 `yaml:"fallback_paraphrase_rate"`
}

// ModelInfo records a model's name and SHA256 digest.
type ModelInfo struct {
	Name string `yaml:"name"`
	SHA  string `yaml:"sha"`
}

// ModelsInfo groups the three model artifacts used by the pipeline.
type ModelsInfo struct {
	Embedder  ModelInfo `yaml:"embedder"`
	Reranker  ModelInfo `yaml:"reranker"`
	Tokenizer ModelInfo `yaml:"tokenizer"`
}

// ConfigMetrics holds the retrieval quality metrics for one config.
type ConfigMetrics struct {
	MRR       float64 `yaml:"mrr"`
	HitAt10   float64 `yaml:"hit_at_10"`
	HitAt1    float64 `yaml:"hit_at_1"`
	RuntimeMS int64   `yaml:"runtime_ms"`
}

// GateInfo records the gate decision and rationale.
type GateInfo struct {
	Pass      bool    `yaml:"pass"`
	WarmRatio float64 `yaml:"warm_ratio"`
	ColdRatio float64 `yaml:"cold_ratio"`
	Reason    string  `yaml:"reason"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

type cliOptions struct {
	corpusPath  string
	anchorsPath string
	k           int
	fast        bool
	reportOut   string
}

func run(args []string) int {
	opts, code := parseOptions(args)
	if code != 0 {
		return code
	}

	return runEval(opts)
}

func parseOptions(args []string) (opts cliOptions, exitCode int) {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	corpusPath := fs.String("corpus", "", "path to corpus JSONL file (required)")
	anchorsPath := fs.String("anchors", "", "path to corpus anchors JSONL file (required)")
	k := fs.Int("k", 10, "top-k cutoff (> 0)")
	fast := fs.Bool("fast", false, "sample N queries based on bench.txt reranker timing")
	reportOut := fs.String("report-out", "eval_report.yaml", "output path for eval_report.yaml")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cliOptions{}, 2
	}
	if *corpusPath == "" {
		fmt.Fprintln(os.Stderr, "error: --corpus is required")
		return cliOptions{}, 2
	}
	if *anchorsPath == "" {
		fmt.Fprintln(os.Stderr, "error: --anchors is required")
		return cliOptions{}, 2
	}
	if *k <= 0 {
		fmt.Fprintf(os.Stderr, "error: --k must be > 0 (got %d)\n", *k)
		return cliOptions{}, 2
	}
	return cliOptions{
		corpusPath:  *corpusPath,
		anchorsPath: *anchorsPath,
		k:           *k,
		fast:        *fast,
		reportOut:   *reportOut,
	}, 0
}

//nolint:funlen // ad hoc eval CLI keeps the linear run/report flow in one place
func runEval(opts cliOptions) int {
	ok, err := eval.HasApprovalMarker(opts.corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: check approval marker: %v\n", err)
		return 3
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "error: corpus is missing \"# APPROVED\" marker")
		return 3
	}

	corpus, err := eval.LoadCorpus(opts.corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load corpus: %v\n", err)
		return 2
	}
	anchors, err := eval.LoadCorpusAnchors(opts.anchorsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load anchors: %v\n", err)
		return 2
	}

	paraphrasePath := filepath.Join(filepath.Dir(opts.corpusPath), "paraphrase_cache.jsonl")

	corpusSHA := fileSHA256(opts.corpusPath)
	anchorsSHA := fileSHA256(opts.anchorsPath)
	paraphraseSHA := fileSHA256(paraphrasePath)
	inputsSHA := computeInputsSHA(opts.corpusPath, opts.anchorsPath, paraphrasePath)

	allQueries := uniqueQueries(corpus)
	queries := allQueries

	if opts.fast {
		benchPath := filepath.Join(filepath.Dir(opts.corpusPath), "bench.txt")
		msPerPair, benchErr := parseBenchTxt(benchPath)
		if benchErr != nil {
			fmt.Fprintf(os.Stderr, "error: parse bench.txt: %v\n", benchErr)
			return 2
		}
		n := sampleN(msPerPair)
		if n < len(allQueries) {
			queries = allQueries[:n]
		}
	}

	sampledCorpus := filterCorpusByQueries(corpus, queries)

	cfgs := []string{"tfidf", "dispatcher-warm", "solo-cli-cold"}
	configs := make(map[string]ConfigMetrics, len(cfgs))
	for _, cfg := range cfgs {
		start := time.Now()
		mrr, hit10, hit1, runErr := eval.RunConfigWithEmbedder(sampledCorpus, anchors, cfg, opts.k)
		runtimeMS := time.Since(start).Milliseconds()
		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: config %q: %v\n", cfg, runErr)
			return 2
		}
		configs[cfg] = ConfigMetrics{
			MRR:       mrr,
			HitAt10:   hit10,
			HitAt1:    hit1,
			RuntimeMS: runtimeMS,
		}
	}

	baseMRR := configs["tfidf"].MRR
	warmMRR := configs["dispatcher-warm"].MRR
	coldMRR := configs["solo-cli-cold"].MRR

	warmRatio, coldRatio := ratios(baseMRR, warmMRR, coldMRR)
	gate := eval.CheckGate(baseMRR, warmMRR, coldMRR)
	gatePass := gate.Pass
	reason := gate.Reason
	if reason == "" {
		reason = gateReason(warmRatio, coldRatio, gatePass)
	}

	report := EvalReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Hardware:  gatherHardware(),
		Corpus: CorpusInfo{
			Seed:                   0,
			CorpusSHA:              corpusSHA,
			AnchorsSHA:             anchorsSHA,
			ParaphraseCacheSHA:     paraphraseSHA,
			InputsSHA:              inputsSHA,
			NumQueriesScored:       len(queries),
			NumAnchors:             len(anchors),
			NumPairs:               len(corpus),
			FallbackParaphraseRate: paraphraseFallbackRate(paraphrasePath, len(queries)),
		},
		Models:  modelInfoFromKnown(),
		Configs: configs,
		Gate: GateInfo{
			Pass:      gatePass,
			WarmRatio: warmRatio,
			ColdRatio: coldRatio,
			Reason:    reason,
		},
	}

	if writeErr := writeReport(opts.reportOut, report); writeErr != nil {
		fmt.Fprintf(os.Stderr, "error: write report: %v\n", writeErr)
		return 2
	}

	if !gatePass {
		fmt.Fprintf(os.Stderr, "\ngate FAILED: %s\n", reason)
		return 1
	}
	fmt.Printf("\ngate PASSED: %s\n", reason)
	return 0
}

func writeReport(path string, r EvalReport) error {
	f, err := os.Create(path) //nolint:gosec // report path is from CLI flag, not user input in untrusted context
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode report %s: %w", path, err)
	}
	return nil
}

func fileSHA256(path string) string {
	f, err := os.Open(path) //nolint:gosec // path is from CLI flag
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// computeInputsSHA = sha256(corpus bytes || anchors bytes || paraphrase_cache bytes).
func computeInputsSHA(corpusPath, anchorsPath, paraphrasePath string) string {
	h := sha256.New()
	for _, p := range []string{corpusPath, anchorsPath, paraphrasePath} {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // path is from CLI flag
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func uniqueQueries(corpus []eval.CorpusEntry) []string {
	seen := make(map[string]struct{}, len(corpus))
	var out []string
	for _, e := range corpus {
		if _, ok := seen[e.Query]; !ok {
			seen[e.Query] = struct{}{}
			out = append(out, e.Query)
		}
	}
	return out
}

func filterCorpusByQueries(corpus []eval.CorpusEntry, queries []string) []eval.CorpusEntry {
	allowed := make(map[string]struct{}, len(queries))
	for _, q := range queries {
		allowed[q] = struct{}{}
	}
	var out []eval.CorpusEntry
	for _, e := range corpus {
		if _, ok := allowed[e.Query]; ok {
			out = append(out, e)
		}
	}
	return out
}

// parseBenchTxt reads "reranker_ms_per_pair: N" from bench.txt.
func parseBenchTxt(path string) (int64, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from corpus flag
	if err != nil {
		return 0, fmt.Errorf("open bench.txt %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != "reranker_ms_per_pair" {
			continue
		}
		ms, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse reranker_ms_per_pair: %w", parseErr)
		}
		return ms, nil
	}
	return 0, fmt.Errorf("reranker_ms_per_pair not found in %s", path)
}

// sampleN computes N = max(5, min(10, floor(30000 / (20 * rerankerMsPerPair)))).
func sampleN(rerankerMsPerPair int64) int {
	if rerankerMsPerPair <= 0 {
		return 10
	}
	n := int(30000 / (20 * rerankerMsPerPair))
	return max(5, min(10, n))
}

func ratios(base, warm, cold float64) (warmRatio, coldRatio float64) {
	if base > 0 {
		warmRatio = warm / base
		coldRatio = cold / base
	}
	return
}

func gateReason(warmRatio, coldRatio float64, pass bool) string {
	if pass {
		return fmt.Sprintf("warm_ratio=%.3f ≥ 1.300, cold_ratio=%.3f ≥ 1.200", warmRatio, coldRatio)
	}
	if warmRatio < 1.30 && coldRatio < 1.20 {
		return fmt.Sprintf("warm_ratio=%.3f < 1.300 AND cold_ratio=%.3f < 1.200", warmRatio, coldRatio)
	}
	if warmRatio < 1.30 {
		return fmt.Sprintf("warm_ratio=%.3f < 1.300", warmRatio)
	}
	return fmt.Sprintf("cold_ratio=%.3f < 1.200", coldRatio)
}

func gatherHardware() HardwareInfo {
	cpu, _ := sysctlString("machdep.cpu.brand_string")
	return HardwareInfo{
		Arch: runtime.GOARCH,
		OS:   runtime.GOOS,
		CPU:  cpu,
	}
}

func sysctlString(name string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "sysctl", "-n", name).Output() //nolint:gosec // name is a compile-time constant
	if err != nil {
		return "", fmt.Errorf("run sysctl %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func modelInfoFromKnown() ModelsInfo {
	var info ModelsInfo
	for _, m := range memory.KnownModels {
		switch m.Name {
		case "bge-small-en-v1.5":
			info.Embedder = ModelInfo{Name: m.Name, SHA: m.SHA256}
		case "bge-reranker-base":
			info.Reranker = ModelInfo{Name: m.Name, SHA: m.SHA256}
		case "bge-tokenizer":
			info.Tokenizer = ModelInfo{Name: m.Name, SHA: m.SHA256}
		}
	}
	return info
}

func paraphraseFallbackRate(paraphrasePath string, numQueries int) float64 {
	if numQueries == 0 {
		return 1.0
	}
	f, err := os.Open(paraphrasePath) //nolint:gosec // path is derived from corpus flag
	if err != nil {
		return 1.0
	}
	defer func() { _ = f.Close() }()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			count++
		}
	}
	hits := min(count, numQueries)
	return float64(numQueries-hits) / float64(numQueries)
}
