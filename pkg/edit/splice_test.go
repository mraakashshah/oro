package edit_test

import (
	"errors"
	"strings"
	"testing"

	"oro/pkg/edit"
)

func TestSpliceAlgorithm(t *testing.T) {
	const goMarker = "// ..."
	const pyMarker = "# ..."

	tests := []struct {
		name    string
		orig    []string
		snippet []string
		marker  string
		want    []string
		wantErr error
	}{
		// ── Eligible cases ───────────────────────────────────────────────────

		{
			name:    "basic replace: new lines between two anchors replace original gap",
			orig:    []string{"a := 1", "old line", "b := 2"},
			snippet: []string{"a := 1", "new line", "b := 2"},
			marker:  goMarker,
			want:    []string{"a := 1", "new line", "b := 2"},
		},
		{
			name:    "continuation marker preserves original gap between anchors",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", goMarker, "b := 2"},
			marker:  goMarker,
			want:    []string{"a := 1", "mid1", "mid2", "b := 2"},
		},
		{
			name:    "new line before continuation marker: insert then preserve",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", "new line", goMarker, "b := 2"},
			marker:  goMarker,
			want:    []string{"a := 1", "new line", "mid1", "mid2", "b := 2"},
		},
		{
			name:    "new line after continuation marker: preserve then insert",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", goMarker, "new line", "b := 2"},
			marker:  goMarker,
			want:    []string{"a := 1", "mid1", "mid2", "new line", "b := 2"},
		},
		{
			name:    "pre-anchor original lines preserved when snippet has no pre-anchor content",
			orig:    []string{"pre1", "a := 1", "old", "b := 2", "post1"},
			snippet: []string{"a := 1", goMarker, "b := 2"},
			marker:  goMarker,
			want:    []string{"pre1", "a := 1", "old", "b := 2", "post1"},
		},
		{
			name:    "pre-anchor original lines replaced when snippet has pre-anchor new lines",
			orig:    []string{"pre1", "a := 1", "old", "b := 2"},
			snippet: []string{"newPre", "a := 1", goMarker, "b := 2"},
			marker:  goMarker,
			want:    []string{"newPre", "a := 1", "old", "b := 2"},
		},
		{
			name:    "post-anchor original lines preserved when snippet ends at last anchor",
			orig:    []string{"a := 1", "mid", "b := 2", "post1", "post2"},
			snippet: []string{"a := 1", "new mid", "b := 2"},
			marker:  goMarker,
			want:    []string{"a := 1", "new mid", "b := 2", "post1", "post2"},
		},
		{
			name:    "post-anchor original lines replaced by snippet post-anchor new lines",
			orig:    []string{"a := 1", "mid", "b := 2", "post1"},
			snippet: []string{"a := 1", goMarker, "b := 2", "newPost"},
			marker:  goMarker,
			want:    []string{"a := 1", "mid", "b := 2", "newPost"},
		},
		{
			name:    "three anchors: replace first gap, preserve second gap",
			orig:    []string{"a", "gap1", "b", "gap2", "c"},
			snippet: []string{"a", "new1", "b", goMarker, "c"},
			marker:  goMarker,
			want:    []string{"a", "new1", "b", "gap2", "c"},
		},
		{
			name:    "adjacent anchors with new lines inserted between them",
			orig:    []string{"a", "b", "c"},
			snippet: []string{"a", "inserted", "b", goMarker, "c"},
			marker:  goMarker,
			want:    []string{"a", "inserted", "b", "c"},
		},
		{
			name:    "EFALLTHROUGH: duplicate anchor text in original is ambiguous",
			orig:    []string{"x", "mid", "x", "end"},
			snippet: []string{"x", "replaced", "x", goMarker},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "unique anchors still splice when unrelated original text repeats",
			orig:    []string{"start", "repeat", "middle", "repeat", "end"},
			snippet: []string{"start", "new middle", "end"},
			marker:  goMarker,
			want:    []string{"start", "new middle", "end"},
		},
		{
			name:    "python continuation marker works the same way",
			orig:    []string{"x = 1", "y = 2", "z = 3"},
			snippet: []string{"x = 1", pyMarker, "z = 3"},
			marker:  pyMarker,
			want:    []string{"x = 1", "y = 2", "z = 3"},
		},
		{
			name:    "empty line in snippet is treated as new line, not anchor",
			orig:    []string{"a", "mid", "b"},
			snippet: []string{"a", "", "b"},
			marker:  goMarker,
			want:    []string{"a", "", "b"},
		},
		{
			name:    "no change: snippet with two anchors and continuation reproduces original",
			orig:    []string{"a", "mid", "b"},
			snippet: []string{"a", goMarker, "b"},
			marker:  goMarker,
			want:    []string{"a", "mid", "b"},
		},
		{
			name:    "continuation in post-anchor segment preserves trailing original lines",
			orig:    []string{"a", "mid", "b", "trail1", "trail2"},
			snippet: []string{"a", goMarker, "b", goMarker},
			marker:  goMarker,
			want:    []string{"a", "mid", "b", "trail1", "trail2"},
		},
		{
			name:    "snippet replaces entire inter-anchor gap with multiple new lines",
			orig:    []string{"start", "old1", "old2", "old3", "end"},
			snippet: []string{"start", "new1", "new2", "end"},
			marker:  goMarker,
			want:    []string{"start", "new1", "new2", "end"},
		},

		// ── EFALLTHROUGH cases ───────────────────────────────────────────────

		{
			name:    "EFALLTHROUGH: zero anchor lines in snippet",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"totally new"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: only one anchor line",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", "new line"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: anchor text not found in original",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{"a := 1", "new", "x := 999"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: anchors in wrong order (reorder detection)",
			orig:    []string{"b := 2", "a := 1"},
			snippet: []string{"a := 1", "new", "b := 2"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: multiple continuation markers between same two anchors",
			orig:    []string{"a := 1", "mid1", "mid2", "b := 2"},
			snippet: []string{"a := 1", goMarker, goMarker, "b := 2"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: empty original body gives zero anchors",
			orig:    []string{},
			snippet: []string{"some line", "other line"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: empty snippet gives zero anchors",
			orig:    []string{"a := 1", "b := 2"},
			snippet: []string{},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: duplicate anchor in original but only one occurrence reachable in order",
			orig:    []string{"a", "b"},
			snippet: []string{"a", "new", "a"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
		{
			name:    "EFALLTHROUGH: two anchors with one ambiguous in original",
			orig:    []string{"a", "mid", "b", "tail", "b"},
			snippet: []string{"a", "new", "b"},
			marker:  goMarker,
			wantErr: edit.ErrFallthrough,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := edit.Splice(tc.orig, tc.snippet, tc.marker)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Splice() error = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("Splice() body = %v, want nil on error", got)
				}
				var fallthroughErr *edit.FallthroughError
				if tc.name == "EFALLTHROUGH: duplicate anchor text in original is ambiguous" && errors.As(err, &fallthroughErr) {
					if !strings.Contains(fallthroughErr.Reason, "ambiguous") {
						t.Fatalf("FallthroughError.Reason = %q, want ambiguous", fallthroughErr.Reason)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("Splice() unexpected error: %v", err)
			}
			if !slicesEqual(got, tc.want) {
				t.Fatalf("Splice() =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
