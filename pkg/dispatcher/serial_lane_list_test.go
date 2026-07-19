package dispatcher

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// serialLaneListPath is the checked-in canonical list of concurrency-flaky
// dispatcher tests that must run only in the serialized quality-gate lane.
const serialLaneListPath = "testdata/serial_lane_tests.txt"

// TestSerialLaneListReproducesUnderContention validates the canonical serial-lane
// list artifact that drives both the RequireSerial guards (oro-0xzy) and the
// quality-gate serial lane (oro-hwx2).
//
// The list itself is derived empirically: see the reproduction command documented
// in testdata/serial_lane_tests.txt. This test does NOT run that reproduction
// (it is non-deterministic by nature); it enforces that the artifact stays
// well-formed, reviewable, and references only real tests, so the list cannot rot.
func TestSerialLaneListReproducesUnderContention(t *testing.T) {
	entries, header := readSerialLaneList(t)

	t.Run("non-empty", func(t *testing.T) {
		if len(entries) == 0 {
			t.Fatalf("%s must list at least one concurrency-flaky test", serialLaneListPath)
		}
	})

	t.Run("sorted and unique (diffable)", func(t *testing.T) {
		seen := map[string]bool{}
		for _, e := range entries {
			if seen[e] {
				t.Errorf("duplicate entry %q — list must be unique", e)
			}
			seen[e] = true
		}
		sorted := append([]string(nil), entries...)
		sort.Strings(sorted)
		for i := range entries {
			if entries[i] != sorted[i] {
				t.Fatalf("list not sorted: entry %d is %q, want %q — keep it sorted so diffs are reviewable", i, entries[i], sorted[i])
			}
		}
	})

	t.Run("every entry names a real dispatcher test", func(t *testing.T) {
		defined := definedTestFuncs(t)
		for _, e := range entries {
			if !defined[e] {
				t.Errorf("listed test %q is not defined in any pkg/dispatcher *_test.go — remove or rename it", e)
			}
		}
	})

	t.Run("documents its reproduction command", func(t *testing.T) {
		// The header must keep a runnable reproduction so future maintainers can
		// re-derive the set rather than trusting a stale list.
		if !strings.Contains(header, "reproduce_serial_lane_flakes.sh") {
			t.Errorf("header must document the reproduction (scripts/reproduce_serial_lane_flakes.sh); got:\n%s", header)
		}
	})
}

// readSerialLaneList returns the test names (non-comment, non-blank lines) and
// the leading comment header block.
func readSerialLaneList(t *testing.T) (entries []string, header string) {
	t.Helper()
	f, err := os.Open(serialLaneListPath)
	if err != nil {
		t.Fatalf("open %s: %v", serialLaneListPath, err)
	}
	defer f.Close()
	var headerLines []string
	inHeader := true
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if inHeader {
				headerLines = append(headerLines, line)
			}
			continue
		}
		inHeader = false
		entries = append(entries, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", serialLaneListPath, err)
	}
	return entries, strings.Join(headerLines, "\n")
}

// definedTestFuncs scans the package's test files for top-level Test functions.
func definedTestFuncs(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	funcRe := regexp.MustCompile(`^func (Test\w+)\(`)
	defined := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := funcRe.FindStringSubmatch(line); m != nil {
				defined[m[1]] = true
			}
		}
	}
	return defined
}
