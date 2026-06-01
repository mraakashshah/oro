package dispatcher //nolint:testpackage // parseTestOutcomes is an unexported pure helper.

import (
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
