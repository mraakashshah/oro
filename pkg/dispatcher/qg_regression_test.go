package dispatcher //nolint:testpackage // parseTestOutcomes is an unexported pure helper.

import (
	"context"
	"errors"
	"reflect"
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
			HeadSHA: "abc123",
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
