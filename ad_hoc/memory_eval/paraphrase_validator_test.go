// ad_hoc/memory_eval/paraphrase_validator_test.go
package memoryeval_test

import (
	"testing"

	memoryeval "oro/ad_hoc/memory_eval"
)

func TestCountSharedContentWords(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{
			// worker/workers lemmatized; crash/crashes lemmatized
			name: "lemmatized_nouns",
			a:    "worker respawns on crash",
			b:    "how do workers recover from crashes",
			want: 2,
		},
		{
			// all stopwords → no content words
			name: "all_stopwords",
			a:    "a the an if",
			b:    "a the an if",
			want: 0,
		},
		{
			// case-folded, stopwords excluded
			name: "case_folded_stopwords_excluded",
			a:    "Go struct field",
			b:    "A Go struct field value",
			want: 3,
		},
		{
			// plural-s-only lemmatizer does NOT handle verb inflections
			name: "verb_inflections_not_lemmatized",
			a:    "run ran running",
			b:    "run",
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := memoryeval.CountSharedContentWords(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("CountSharedContentWords(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
