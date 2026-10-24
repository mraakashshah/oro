package factory_test

import (
	"strings"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestCanonicalDockerCreatedAt(t *testing.T) {
	t.Run("preserves the exact daemon instant", func(t *testing.T) {
		cases := map[string]string{
			"2026-10-24T01:49:04-04:00":           "2026-10-24T05:49:04Z",
			"2026-10-24T05:49:04Z":                "2026-10-24T05:49:04Z",
			"2026-10-24T05:49:04+00:00":           "2026-10-24T05:49:04Z",
			"2026-10-24T05:49:04-00:00":           "2026-10-24T05:49:04Z",
			"2026-10-24T11:34:04+05:45":           "2026-10-24T05:49:04Z",
			"2026-10-24T00:15:04+05:30":           "2026-10-23T18:45:04Z",
			"2026-12-31T23:59:59-00:01":           "2027-01-01T00:00:59Z",
			"2024-02-29T00:00:00Z":                "2024-02-29T00:00:00Z",
			"2026-10-24T01:49:04.000000000-04:00": "2026-10-24T05:49:04Z",
			"2026-10-24T01:49:04.123400000-04:00": "2026-10-24T05:49:04.1234Z",
			"2026-10-24T01:49:04.123456789-04:00": "2026-10-24T05:49:04.123456789Z",
			"2026-10-24T05:49:04.1Z":              "2026-10-24T05:49:04.1Z",
			"0001-01-01T00:00:00.000000001Z":      "0001-01-01T00:00:00.000000001Z",
			"9999-12-31T23:59:59.999999999Z":      "9999-12-31T23:59:59.999999999Z",
			"2026-10-24T23:59:00+23:59":           "2026-10-24T00:00:00Z",
			"2026-10-24T00:00:00-23:59":           "2026-10-24T23:59:00Z",
		}
		for input, want := range cases {
			t.Run(input, func(t *testing.T) {
				got, err := factory.CanonicalDockerCreatedAt(input)
				if err != nil || got != want {
					t.Fatalf("canonical creation time = %q, %v; want %q", got, err, want)
				}
				original, err := time.Parse(time.RFC3339Nano, input)
				if err != nil {
					t.Fatal(err)
				}
				canonical, err := time.Parse(time.RFC3339Nano, got)
				if err != nil || !canonical.Equal(original) || len(got) > 30 {
					t.Fatalf("normalization changed instant or exceeded canonical bound: %q, %v", got, err)
				}
				again, err := factory.CanonicalDockerCreatedAt(got)
				if err != nil || again != got {
					t.Fatalf("normalization is not idempotent: %q, %v", again, err)
				}
			})
		}
	})
	t.Run("strict bounded lexical and calendar input", func(t *testing.T) {
		for _, input := range []string{
			"", strings.Repeat("0", 1<<20),
			" 2026-10-24T05:49:04Z", "2026-10-24T05:49:04Z ", "2026-10-24T05:49:04Z\n", "2026-10-24T05:49:04Z\x00",
			"2026-10-24T05:49:04Zjunk", "2026-10-24T05:49:04Z+00:00",
			"2026-10-24T5:49:04Z", "2026-10-24T05:9:04Z", "2026-10-24T05:49:4Z",
			"2026-1-24T05:49:04Z", "2026-10-4T05:49:04Z", "026-10-24T05:49:04Z", "10000-10-24T05:49:04Z",
			"2026-10-24 05:49:04Z", "2026-10-24t05:49:04Z", "2026-10-24T05:49:04z",
			"2026-10-24T05:49:04,123Z", "2026-10-24T05:49:04.Z", "2026-10-24T05:49:04.1234567890Z",
			"2026-10-24T05:49:04.1234567890+00:00", "2026-10-24T05:49:04.１２３Z",
			"2026-10-24T05:49:04", "2026-10-24T05:49:04+0:00", "2026-10-24T05:49:04+00:0",
			"2026-10-24T05:49:04+0000", "2026-10-24T05:49:04+00:00:00", "2026-10-24T05:49:04+24:00",
			"2026-10-24T05:49:04-24:00", "2026-10-24T05:49:04+00:60", "2026-10-24T05:49:04-00:60",
			"2026-10-24T24:00:00Z", "2026-10-24T05:60:00Z", "2026-10-24T05:49:60Z",
			"2026-00-24T05:49:04Z", "2026-13-24T05:49:04Z", "2026-10-00T05:49:04Z", "2026-10-32T05:49:04Z",
			"2026-02-29T05:49:04Z", "1900-02-29T05:49:04Z", "2026-04-31T05:49:04Z",
			"0001-01-01T00:00:00Z", "0001-01-01T00:00:00.000000000+00:00", "0001-01-01T01:00:00+01:00",
			"0000-01-01T00:00:00Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01",
		} {
			got, err := factory.CanonicalDockerCreatedAt(input)
			if err == nil || got != "" {
				t.Errorf("invalid creation time (length %d) accepted: %.80q => %q, %v", len(input), input, got, err)
			}
		}
	})
}
