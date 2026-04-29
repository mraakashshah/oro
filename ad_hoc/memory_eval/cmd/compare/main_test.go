//go:build integration && cgo && darwin

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCompareMissingApprovalMarkerIncludesCorpusPath(t *testing.T) {
	dir := t.TempDir()
	corpusPath := filepath.Join(dir, "unapproved_corpus.jsonl")
	if err := os.WriteFile(corpusPath, []byte("# source: fixture\n"), 0o600); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	stderr := captureStderr(t, func() {
		code := run([]string{
			"--corpus", corpusPath,
			"--anchors", "testdata/corpus_anchors.jsonl",
		})
		if code != 3 {
			t.Errorf("exit code = %d, want 3", code)
		}
	})

	if !strings.Contains(stderr, corpusPath) {
		t.Fatalf("stderr %q does not include corpus path %q", stderr, corpusPath)
	}
	if !strings.Contains(stderr, "missing") || !strings.Contains(stderr, "# APPROVED") {
		t.Fatalf("stderr %q does not describe the missing approval marker", stderr)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	oldStderr := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = writeEnd
	defer func() {
		os.Stderr = oldStderr
	}()

	fn()

	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	data, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := readEnd.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return string(data)
}

func TestCompareFastCompletesUnder30s(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "eval_report.yaml")

	start := time.Now()
	code := run([]string{
		"--corpus", "testdata/corpus_no_relevant.jsonl",
		"--anchors", "testdata/corpus_anchors.jsonl",
		"--fast",
		"--report-out", reportPath,
	})
	elapsed := time.Since(start)

	if elapsed >= 30*time.Second {
		t.Errorf("--fast took %v, want < 30s", elapsed)
	}
	// With unlabeled corpus all MRR = 0, so the zero-baseline gate policy fails.
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (gate should fail with all-zero MRR)", code)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Errorf("eval_report.yaml not written: %v", err)
	}
}

func TestCompareExitCodeReflectsGate(t *testing.T) {
	t.Run("zero_baseline_exits_1_and_writes_yaml_with_pass_false", func(t *testing.T) {
		dir := t.TempDir()
		reportPath := filepath.Join(dir, "eval_report.yaml")

		code := run([]string{
			"--corpus", "testdata/corpus_no_relevant.jsonl",
			"--anchors", "testdata/corpus_anchors.jsonl",
			"--report-out", reportPath,
		})

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		data, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("read report: %v", err)
		}
		var report EvalReport
		if err := yaml.Unmarshal(data, &report); err != nil {
			t.Fatalf("unmarshal report: %v", err)
		}
		if report.Gate.Pass {
			t.Errorf("gate.pass = true, want false")
		}
		if !strings.Contains(report.Gate.Reason, "baseline MRR is 0") {
			t.Errorf("gate.reason = %q, want zero-baseline failure", report.Gate.Reason)
		}
	})

	t.Run("gate_fail_exits_1_and_writes_yaml_with_pass_false", func(t *testing.T) {
		dir := t.TempDir()
		reportPath := filepath.Join(dir, "eval_report.yaml")

		// With labeled relevant entries, all configs get similar MRR (same embedder
		// for tfidf and dispatcher-warm), so warm_ratio = 1.0 < 1.30 → gate fails.
		code := run([]string{
			"--corpus", "testdata/corpus_with_relevant.jsonl",
			"--anchors", "testdata/corpus_anchors.jsonl",
			"--report-out", reportPath,
		})

		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		data, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("read report: %v", err)
		}
		var report EvalReport
		if err := yaml.Unmarshal(data, &report); err != nil {
			t.Fatalf("unmarshal report: %v", err)
		}
		if report.Gate.Pass {
			t.Errorf("gate.pass = true, want false")
		}
	})
}

func TestEvalReportYAMLSchema(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "eval_report.yaml")

	run([]string{
		"--corpus", "testdata/corpus_no_relevant.jsonl",
		"--anchors", "testdata/corpus_anchors.jsonl",
		"--report-out", reportPath,
	})

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	var report EvalReport
	if err := yaml.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	// timestamp
	if report.Timestamp == "" {
		t.Error("timestamp is empty")
	}

	// hardware
	if report.Hardware.Arch == "" {
		t.Error("hardware.arch is empty")
	}
	if report.Hardware.OS == "" {
		t.Error("hardware.os is empty")
	}
	if report.Hardware.CPU == "" {
		t.Error("hardware.cpu is empty")
	}

	// corpus
	if report.Corpus.CorpusSHA == "" {
		t.Error("corpus.corpus_sha is empty")
	}
	if report.Corpus.AnchorsSHA == "" {
		t.Error("corpus.anchors_sha is empty")
	}
	if report.Corpus.InputsSHA == "" {
		t.Error("corpus.inputs_sha is empty")
	}
	if report.Corpus.NumAnchors == 0 {
		t.Error("corpus.num_anchors is 0")
	}
	if report.Corpus.NumPairs == 0 {
		t.Error("corpus.num_pairs is 0")
	}

	// models
	if report.Models.Embedder.Name == "" {
		t.Error("models.embedder.name is empty")
	}
	if report.Models.Embedder.SHA == "" {
		t.Error("models.embedder.sha is empty")
	}
	if report.Models.Reranker.Name == "" {
		t.Error("models.reranker.name is empty")
	}
	if report.Models.Tokenizer.Name == "" {
		t.Error("models.tokenizer.name is empty")
	}

	// configs
	for _, cfg := range []string{"tfidf", "dispatcher-warm", "solo-cli-cold"} {
		m, ok := report.Configs[cfg]
		if !ok {
			t.Errorf("configs.%s missing", cfg)
			continue
		}
		if m.RuntimeMS < 0 {
			t.Errorf("configs.%s.runtime_ms < 0", cfg)
		}
	}

	// gate
	if report.Gate.Reason == "" {
		t.Error("gate.reason is empty")
	}
}
