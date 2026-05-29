// Package edit provides deterministic AST-aware editing for Go, Python, TypeScript,
// and JavaScript. This file implements the language-agnostic anchor-splice core (§7.3).
package edit

import "errors"

// ErrFallthrough is returned when a snippet is ineligible for anchor splice.
// The caller should fall through to a native Edit operation.
var ErrFallthrough = errors.New("EFALLTHROUGH")

type lineKind int

const (
	lineNew    lineKind = iota // does not match any original body line
	lineAnchor                 // exactly matches a non-empty original body line
	lineCont                   // continuation marker: "preserve original lines here"
)

const maxMarkerlessDrop = 20

type classifiedLine struct {
	text string
	kind lineKind
}

// classifySnippet assigns each snippet line a kind.
// Continuation marker takes priority over anchor classification.
// Empty lines are always lineNew (spec: "exactly matches a non-empty line").
func classifySnippet(orig, snippet []string, contMarker string) []classifiedLine {
	origSet := make(map[string]struct{}, len(orig))
	for _, l := range orig {
		if l != "" {
			origSet[l] = struct{}{}
		}
	}
	out := make([]classifiedLine, len(snippet))
	for i, l := range snippet {
		switch {
		case l == contMarker:
			out[i] = classifiedLine{text: l, kind: lineCont}
		case l != "":
			if _, ok := origSet[l]; ok {
				out[i] = classifiedLine{text: l, kind: lineAnchor}
			} else {
				out[i] = classifiedLine{text: l, kind: lineNew}
			}
		default:
			out[i] = classifiedLine{text: l, kind: lineNew}
		}
	}
	return out
}

// findAnchorPositions maps each anchor in classified (snippet order) to its position
// in orig using forward-progress search, ensuring original-body order is respected.
func findAnchorPositions(orig []string, classified []classifiedLine) ([]int, error) {
	origCount := make(map[string]int, len(orig))
	for _, l := range orig {
		if l != "" {
			origCount[l]++
		}
	}

	positions := make([]int, 0)
	searchFrom := 0
	for _, cl := range classified {
		if cl.kind != lineAnchor {
			continue
		}
		if origCount[cl.text] > 1 {
			return nil, &FallthroughError{Reason: "ambiguous anchor: line occurs more than once in original body"}
		}
		found := -1
		for i := searchFrom; i < len(orig); i++ {
			if orig[i] == cl.text {
				found = i
				searchFrom = i + 1
				break
			}
		}
		if found == -1 {
			return nil, &FallthroughError{Reason: "anchor text not found in original body"}
		}
		positions = append(positions, found)
	}
	return positions, nil
}

// splitByAnchors partitions classified into:
//   - pre: lines before the first anchor
//   - inter: segments between consecutive anchors (len = len(anchors)-1)
//   - post: lines after the last anchor
func splitByAnchors(classified []classifiedLine) (pre []classifiedLine, inter [][]classifiedLine, post []classifiedLine) {
	pre = make([]classifiedLine, 0)
	inter = make([][]classifiedLine, 0)
	current := make([]classifiedLine, 0)
	anchorSeen := false
	for _, cl := range classified {
		if cl.kind == lineAnchor {
			if !anchorSeen {
				pre = current
				anchorSeen = true
			} else {
				inter = append(inter, current)
			}
			current = make([]classifiedLine, 0)
		} else {
			current = append(current, cl)
		}
	}
	post = current
	return pre, inter, post
}

// contCount returns the number of continuation markers in seg.
func contCount(seg []classifiedLine) int {
	n := 0
	for _, cl := range seg {
		if cl.kind == lineCont {
			n++
		}
	}
	return n
}

// validateEligibility applies the three eligibility rules from §7.3:
//  1. At least 2 anchor lines.
//  2. Anchors appear in original-body order (enforced by findAnchorPositions).
//  3. Continuation markers are unambiguous: at most one per segment.
func validateEligibility(anchorPositions []int, pre []classifiedLine, inter [][]classifiedLine, post []classifiedLine) error {
	if len(anchorPositions) < 2 {
		reason := "no anchor lines matched; need at least 2."
		if len(anchorPositions) == 1 {
			reason = "only 1 anchor line matched; need at least 2."
		}
		return &FallthroughError{Reason: reason}
	}
	if contCount(pre) > 1 || contCount(post) > 1 {
		return &FallthroughError{Reason: "ambiguous continuation markers: more than one per segment"}
	}
	for _, seg := range inter {
		if contCount(seg) > 1 {
			return &FallthroughError{Reason: "ambiguous continuation markers: more than one per segment"}
		}
	}
	return nil
}

// processSegment produces the output lines for a single region.
//
// Rules:
//   - No continuation marker, empty segment  → preserve origRegion unchanged.
//   - No continuation marker, non-empty segment → replace origRegion with new lines.
//   - One continuation marker → linesBeforeMarker + origRegion + linesAfterMarker.
func processSegment(seg []classifiedLine, origRegion []string) ([]string, error) {
	contIdx := -1
	for i, cl := range seg {
		if cl.kind == lineCont {
			contIdx = i
			break
		}
	}

	if contIdx == -1 {
		newLines := make([]string, 0, len(seg))
		for _, cl := range seg {
			newLines = append(newLines, cl.text)
		}
		if len(newLines) == 0 {
			return origRegion, nil
		}
		if len(origRegion) > maxMarkerlessDrop {
			return nil, &FallthroughError{Reason: "markerless replacement spans more than 20 original lines; add a continuation marker to preserve original lines explicitly"}
		}
		return newLines, nil
	}

	result := make([]string, 0, len(seg)-1+len(origRegion))
	for _, cl := range seg[:contIdx] {
		result = append(result, cl.text)
	}
	result = append(result, origRegion...)
	for _, cl := range seg[contIdx+1:] {
		result = append(result, cl.text)
	}
	return result, nil
}

// Splice applies the anchor-splice algorithm described in §7.3.
//
// orig is the current body lines of a symbol (without signature/braces).
// snippet is the replacement: a mix of anchor lines, new lines, and continuation markers.
// contMarker is the language-specific continuation marker ("// ..." or "# ...").
//
// Returns the new body lines, or ErrFallthrough if the snippet is ineligible.
// ErrFallthrough is always returned with a nil body slice.
//
//oro:testonly — wired from production by the pkg/edit CLI surface bead (Phase C.2)
func Splice(orig, snippet []string, contMarker string) ([]string, error) {
	classified := classifySnippet(orig, snippet, contMarker)

	anchorPositions, err := findAnchorPositions(orig, classified)
	if err != nil {
		return nil, err
	}

	pre, inter, post := splitByAnchors(classified)

	if err := validateEligibility(anchorPositions, pre, inter, post); err != nil {
		return nil, err
	}

	anchorCount := len(anchorPositions)
	firstPos := anchorPositions[0]
	lastPos := anchorPositions[anchorCount-1]

	var result []string

	// Region before first anchor.
	preLines, err := processSegment(pre, orig[:firstPos])
	if err != nil {
		return nil, err
	}
	result = append(result, preLines...)

	// Each anchor line, followed by the inter-anchor gap (if not the last anchor).
	for i, anchorPos := range anchorPositions {
		result = append(result, orig[anchorPos])
		if i < anchorCount-1 {
			nextPos := anchorPositions[i+1]
			interLines, err := processSegment(inter[i], orig[anchorPos+1:nextPos])
			if err != nil {
				return nil, err
			}
			result = append(result, interLines...)
		}
	}

	// Region after last anchor.
	postLines, err := processSegment(post, orig[lastPos+1:])
	if err != nil {
		return nil, err
	}
	result = append(result, postLines...)

	return result, nil
}
