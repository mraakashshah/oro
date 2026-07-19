package dispatcher

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requireSerialCall matches the guard call that opts a test into the serial lane.
var requireSerialCall = regexp.MustCompile(`\bqgserial\.RequireSerial\(`)

// directListener matches a test that stands up a real listener directly in its own
// body (the subset that IS statically detectable — see the scope note below).
var directListener = regexp.MustCompile(`\bnet\.Listen(Unix)?\(`)

// TestFlakyTimingTestsCarrySerialGuard enforces the serial-lane invariant so a
// concurrency-flaky test cannot silently rejoin the concurrent main gate.
//
// It checks two directions:
//
//   - Every test named in testdata/serial_lane_tests.txt carries a
//     qgserial.RequireSerial(t) guard. This is the reliable, maintained direction:
//     the canonical list (oro-sjp8) is the source of truth for the flaky set and
//     the guards must match it.
//
//   - Every test that opens a real net listener DIRECTLY in its own body carries
//     the guard (or is on the list). SCOPE: this static backstop reliably covers
//     only the direct-listener subset. The timing-only flaky class (waitFor /
//     deadline tests that go through shared helpers) is indistinguishable from
//     many benign tests by static scan, so it is covered by the maintained list,
//     NOT by this test. Do not read a pass here as "no unguarded timing flaky can
//     exist" — only the direct-listener subset is machine-checkable.
func TestFlakyTimingTestsCarrySerialGuard(t *testing.T) {
	bodies := dispatcherTestFuncBodies(t)
	listed, _ := readSerialLaneList(t)

	t.Run("every listed test carries the guard", func(t *testing.T) {
		for _, name := range listed {
			body, ok := bodies[name]
			if !ok {
				t.Errorf("listed test %q not found in package", name)
				continue
			}
			if !requireSerialCall.MatchString(body) {
				t.Errorf("test %q is on the serial-lane list but lacks a qgserial.RequireSerial(t) guard", name)
			}
		}
	})

	t.Run("direct-listener tests carry the guard", func(t *testing.T) {
		for name, body := range bodies {
			if directListener.MatchString(body) && !requireSerialCall.MatchString(body) {
				t.Errorf("test %q opens a real net listener but lacks qgserial.RequireSerial(t) — add the guard and list it in %s", name, serialLaneListPath)
			}
		}
	})
}

// dispatcherTestFuncBodies returns a map of top-level Test function name to its
// full source body (brace-balanced) across the package's *_test.go files.
func dispatcherTestFuncBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	funcRe := regexp.MustCompile(`^func (Test\w+)\(`)
	bodies := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		lines := strings.Split(string(data), "\n")
		for i := 0; i < len(lines); i++ {
			m := funcRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			name := m[1]
			var body []string
			depth, started := 0, false
			j := i
			for ; j < len(lines); j++ {
				body = append(body, lines[j])
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
				if strings.Contains(lines[j], "{") {
					started = true
				}
				if started && depth <= 0 {
					break
				}
			}
			bodies[name] = strings.Join(body, "\n")
			i = j
		}
	}
	return bodies
}
