package edit

import "testing"

func TestSplitByAnchorsReturnsNonNilEmptySegments(t *testing.T) {
	classified := []classifiedLine{
		{text: "a", kind: lineAnchor},
		{text: "b", kind: lineAnchor},
	}

	pre, inter, post := splitByAnchors(classified)
	if pre == nil {
		t.Fatal("pre segment is nil, want non-nil empty slice")
	}
	if post == nil {
		t.Fatal("post segment is nil, want non-nil empty slice")
	}
	if inter == nil {
		t.Fatal("inter segments are nil, want non-nil empty slice")
	}
	if len(inter) != 1 {
		t.Fatalf("inter length = %d, want 1 empty segment between adjacent anchors", len(inter))
	}
	if inter[0] == nil {
		t.Fatal("inter[0] is nil, want non-nil empty slice")
	}
	if len(pre) != 0 || len(inter[0]) != 0 || len(post) != 0 {
		t.Fatalf("got lengths pre=%d inter[0]=%d post=%d, want all 0", len(pre), len(inter[0]), len(post))
	}
}

func TestFindAnchorPositionsReturnsNonNilEmptySlice(t *testing.T) {
	positions, err := findAnchorPositions([]string{"a"}, []classifiedLine{{text: "x", kind: lineNew}})
	if err != nil {
		t.Fatalf("findAnchorPositions: %v", err)
	}
	if positions == nil {
		t.Fatal("positions is nil, want non-nil empty slice")
	}
	if len(positions) != 0 {
		t.Fatalf("positions length = %d, want 0", len(positions))
	}
}
