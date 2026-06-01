package dispatcher //nolint:testpackage // parseTestOutcomes is an unexported pure helper.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTestOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   map[string]bool
	}{
		{
			name: "go test pass fail lines",
			output: `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- FAIL: TestB (0.01s)
FAIL
`,
			want: map[string]bool{
				"TestA": true,
				"TestB": false,
			},
		},
		{
			name: "pytest pass fail lines",
			output: `test_a PASSED
test_b FAILED
tests/test_sample.py::test_c PASSED
`,
			want: map[string]bool{
				"test_a": true,
				"test_b": false,
				"test_c": true,
			},
		},
		{
			name:   "unrecognized lines ignored",
			output: "ok  \toro/pkg/dispatcher\t0.313s\nsome random output\n",
			want:   map[string]bool{},
		},
		{
			name:   "empty output",
			output: "",
			want:   map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parseTestOutcomes(tt.output)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseTestOutcomes() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCaptureQGBaseline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	worktree := t.TempDir()
	runner := &mockCommandRunner{output: []byte("abc123\n")}
	qgRunner := &mockQGRunner{
		passed: true,
		output: `=== RUN   TestKept
--- PASS: TestKept (0.00s)
=== RUN   TestBroken
--- FAIL: TestBroken (0.01s)
FAIL
`,
	}
	d := &Dispatcher{
		shutdownRunner: runner,
		qgRunner:       qgRunner,
	}

	baseline, err := d.captureQGBaseline(ctx, "bead-1", worktree)
	if err != nil {
		t.Fatalf("captureQGBaseline() error = %v", err)
	}

	want := qgBaseline{
		"bead-1": {
			HeadSHA:     "abc123",
			SuitePassed: true,
			Outcomes: map[string]bool{
				"TestKept":   true,
				"TestBroken": false,
			},
		},
	}
	if !reflect.DeepEqual(baseline, want) {
		t.Fatalf("captureQGBaseline() = %#v, want %#v", baseline, want)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("git calls = %d, want 1", len(runner.calls))
	}
	if runner.calls[0].Name != "git" || !reflect.DeepEqual(runner.calls[0].Args, []string{"-C", worktree, "rev-parse", "HEAD"}) {
		t.Fatalf("git call = %#v", runner.calls[0])
	}
	if len(qgRunner.calls) != 1 || qgRunner.calls[0] != worktree {
		t.Fatalf("qg calls = %#v, want [%q]", qgRunner.calls, worktree)
	}

	cached, err := d.captureQGBaseline(ctx, "bead-1", worktree)
	if err != nil {
		t.Fatalf("cached captureQGBaseline() error = %v", err)
	}
	if !reflect.DeepEqual(cached, want) {
		t.Fatalf("cached captureQGBaseline() = %#v, want %#v", cached, want)
	}
	if len(qgRunner.calls) != 1 {
		t.Fatalf("cached capture re-ran QG %d times, want 1", len(qgRunner.calls))
	}
}

func TestCaptureQGBaselineGitFailure(t *testing.T) {
	t.Parallel()

	proofErr := errors.New("rev-parse failed")
	d := &Dispatcher{
		shutdownRunner: &mockCommandRunner{err: proofErr},
		qgRunner:       &mockQGRunner{passed: true, output: "--- PASS: TestA (0.00s)\n"},
	}

	_, err := d.captureQGBaseline(context.Background(), "bead-1", t.TempDir())
	if !errors.Is(err, proofErr) {
		t.Fatalf("captureQGBaseline() error = %v, want %v", err, proofErr)
	}
	if len(d.qgBaselineCache) != 0 {
		t.Fatalf("qgBaselineCache = %#v, want empty", d.qgBaselineCache)
	}
}

func TestCompareQGRegressionBaseline(t *testing.T) {
	t.Parallel()

	baseline := qgBaseline{
		"bead-1": {
			HeadSHA: "abc123",
			Outcomes: map[string]bool{
				"TestStillGreen": true,
				"TestRegressed":  true,
				"TestWasRed":     false,
			},
		},
	}

	got := compareQGRegressionBaseline(baseline, "bead-1", map[string]bool{
		"TestStillGreen": true,
		"TestRegressed":  false,
		"TestWasRed":     false,
	})
	want := []qgRegression{
		{
			TestName:       "TestRegressed",
			BaselinePassed: true,
			CurrentPassed:  false,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compareQGRegressionBaseline() = %#v, want %#v", got, want)
	}
}

func TestDetectQGRegression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		baseline   qgBaseline
		postPassed bool
		postOutput string
		want       qgRegression
	}{
		{
			name: "green to red test is regression",
			baseline: qgBaseline{
				"bead-1": {
					HeadSHA: "abc123",
					Outcomes: map[string]bool{
						"TestA": true,
						"TestB": true,
					},
				},
			},
			postPassed: false,
			postOutput: `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- FAIL: TestB (0.01s)
FAIL
`,
			want: qgRegression{
				TestName:       "TestB",
				BaselinePassed: true,
				CurrentPassed:  false,
			},
		},
		{
			name: "pre-existing red is not regression",
			baseline: qgBaseline{
				"bead-1": {
					HeadSHA: "abc123",
					Outcomes: map[string]bool{
						"TestB": false,
					},
				},
			},
			postPassed: false,
			postOutput: `=== RUN   TestB
--- FAIL: TestB (0.01s)
FAIL
`,
		},
		{
			name: "identical outcomes are not regression",
			baseline: qgBaseline{
				"bead-1": {
					HeadSHA: "abc123",
					Outcomes: map[string]bool{
						"TestA": true,
						"TestB": false,
					},
				},
			},
			postPassed: false,
			postOutput: `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- FAIL: TestB (0.01s)
FAIL
`,
		},
		{
			name: "unparseable green to red falls back to suite regression",
			baseline: qgBaseline{
				"bead-1": {
					HeadSHA:     "abc123",
					SuitePassed: true,
					Outcomes:    map[string]bool{},
				},
			},
			postPassed: false,
			postOutput: "quality gate failed without parseable test names",
			want: qgRegression{
				TestName:       "quality_gate",
				BaselinePassed: true,
				CurrentPassed:  false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := &Dispatcher{
				qgRunner: &mockQGRunner{
					passed: tt.postPassed,
					output: tt.postOutput,
				},
			}

			got, err := d.detectQGRegression(context.Background(), tt.baseline, t.TempDir())
			if err != nil {
				t.Fatalf("detectQGRegression() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("detectQGRegression() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRevertRegressedRetry_IssuesResetHard(t *testing.T) {
	ctx := context.Background()
	worktree := t.TempDir()
	binDir := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "git-call")
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$PWD\" \"$*\" > " + recordPath + "\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	base := qgBaseline{
		"bead-1": {
			HeadSHA: "abc123",
		},
	}

	d := &Dispatcher{}
	if err := d.revertRegressedRetry(ctx, base, worktree); err != nil {
		t.Fatalf("revertRegressedRetry() error = %v", err)
	}

	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read fake git record: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(record)), "\n")
	if len(lines) != 2 {
		t.Fatalf("record = %q, want dir and args lines", record)
	}
	if lines[0] != worktree {
		t.Fatalf("git Dir = %q, want %q", lines[0], worktree)
	}
	if lines[1] != "reset --hard abc123" {
		t.Fatalf("git args = %q, want %q", lines[1], "reset --hard abc123")
	}
}

func TestRevertRegressedRetry_ReturnsResetFailure(t *testing.T) {
	ctx := context.Background()
	worktree := t.TempDir()
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexit 42\n"), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	base := qgBaseline{
		"bead-1": {
			HeadSHA: "abc123",
		},
	}

	d := &Dispatcher{}
	err := d.revertRegressedRetry(ctx, base, worktree)
	if err == nil {
		t.Fatal("revertRegressedRetry() error = nil, want reset failure")
	}
	if !strings.Contains(err.Error(), "revert regressed retry") {
		t.Fatalf("revertRegressedRetry() error = %v, want revert context", err)
	}
}
