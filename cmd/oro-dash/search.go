package main

import (
	"fmt"
	"strconv"
	"strings"
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

// matchesBead checks if a bead matches the given filters.
func matchesBead(bead Bead, filters searchFilters) bool {
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
			if !strings.Contains(searchableText, term) {
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
func (s *SearchModel) Filter(beads []Bead, query string) []Bead {
	if query == "" {
		return beads
	}

	filters := parseQuery(query)

	var result []Bead
	for _, bead := range beads {
		if matchesBead(bead, filters) {
			result = append(result, bead)
		}
	}

	return result
}
