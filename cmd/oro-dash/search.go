package main

import (
	"fmt"
	"strconv"
	"strings"

	"oro/pkg/protocol"
)

// SearchModel handles search and filtering logic for beads.
type SearchModel struct{}

// searchFilters represents parsed search query filters.
type searchFilters struct {
	priority   *int
	status     string
	beadType   string
	fuzzyTerms []string
}

// parseQuery parses a search query into filters and fuzzy terms.
func parseQuery(query string) searchFilters {
	var filters searchFilters
	parts := strings.Fields(query)

	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, "p:"):
			if p, err := strconv.Atoi(strings.TrimPrefix(part, "p:")); err == nil {
				filters.priority = &p
			}
		case strings.HasPrefix(part, "s:"):
			filters.status = strings.TrimPrefix(part, "s:")
		case strings.HasPrefix(part, "t:"):
			filters.beadType = strings.TrimPrefix(part, "t:")
		default:
			filters.fuzzyTerms = append(filters.fuzzyTerms, strings.ToLower(part))
		}
	}

	return filters
}

// levenshtein returns the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Use two-row DP to save memory.
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1]
			} else {
				curr[j] = 1 + min(prev[j], min(curr[j-1], prev[j-1]))
			}
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// minEditDistance returns the minimum edit distance between needle and any
// substring of haystack. An empty needle costs 0; an empty haystack costs
// len(needle).
func minEditDistance(needle, haystack string) int {
	n, m := len(needle), len(haystack)
	if n == 0 {
		return 0
	}
	if m == 0 {
		return n
	}
	// dp[j] = min edit distance to align needle[0..i-1] against any substring
	// of haystack ending at position j-1.
	// Initialise row 0 to 0: we can start matching anywhere in haystack for free.
	dp := make([]int, m+1)
	// dp[0..m] = 0 by default (zero value of int slice).

	for i := 1; i <= n; i++ {
		// prevDiag is dp[i-1][j-1] before the current row overwrites it.
		prevDiag := 0 // dp[i-1][0] — cost of aligning i chars of needle with empty haystack prefix = i, but for row 0 it was 0; for row i it is i.
		// Actually we need dp[i][0] = i (delete all needle chars matched against empty start).
		prev0 := i - 1 // dp[i-1][0]
		newFirst := i  // dp[i][0]
		prevDiag = prev0
		dp[0] = newFirst
		for j := 1; j <= m; j++ {
			oldDp := dp[j] // dp[i-1][j] before overwrite
			if needle[i-1] == haystack[j-1] {
				dp[j] = prevDiag
			} else {
				dp[j] = 1 + min(dp[j], min(dp[j-1], prevDiag))
			}
			prevDiag = oldDp
		}
	}

	// Minimum over last row — minimum cost to match all of needle against any
	// suffix-ending position in haystack.
	result := dp[0]
	for j := 1; j <= m; j++ {
		if dp[j] < result {
			result = dp[j]
		}
	}
	return result
}

// fuzzyMaxDistance returns the maximum edit distance allowed for a term of the
// given length. Terms of 1-4 chars use exact substring matching (distance 0)
// to avoid false positives on short terms. Terms of 5+ chars allow one edit.
func fuzzyMaxDistance(termLen int) int {
	if termLen <= 4 {
		return 0
	}
	return 1
}

// fuzzyMatchTerm reports whether term approximately matches text.
// The allowed edit distance is determined by fuzzyMaxDistance.
func fuzzyMatchTerm(term, text string) bool {
	return minEditDistance(term, text) <= fuzzyMaxDistance(len(term))
}

// matchesBead checks if a bead matches the given filters.
func matchesBead(bead protocol.Bead, filters searchFilters) bool {
	// Check priority filter
	if filters.priority != nil && bead.Priority != *filters.priority {
		return false
	}

	// Check status filter
	if filters.status != "" && !strings.EqualFold(bead.Status, filters.status) {
		return false
	}

	// Check type filter
	if filters.beadType != "" && !strings.EqualFold(bead.Type, filters.beadType) {
		return false
	}

	// Check fuzzy match
	if len(filters.fuzzyTerms) > 0 {
		searchableText := strings.ToLower(fmt.Sprintf("%s %s", bead.ID, bead.Title))
		for _, term := range filters.fuzzyTerms {
			if !fuzzyMatchTerm(term, searchableText) {
				return false
			}
		}
	}

	return true
}

// Filter filters beads based on a search query.
// Supports fuzzy matching on ID and title, and filter prefixes:
// - p:N for priority (e.g., p:0)
// - s:STATUS for status (e.g., s:open)
// - t:TYPE for type (e.g., t:bug)
// Empty query returns all beads.
func (s *SearchModel) Filter(beads []protocol.Bead, query string) []protocol.Bead {
	if query == "" {
		return beads
	}

	filters := parseQuery(query)

	var result []protocol.Bead
	for _, bead := range beads {
		if matchesBead(bead, filters) {
			result = append(result, bead)
		}
	}

	return result
}
