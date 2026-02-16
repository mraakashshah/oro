package protocol

import "strings"

// SanitizeFTS5Query wraps each term in double quotes to prevent FTS5 operator
// interpretation (e.g., "and", "or", "not" are FTS5 operators) and joins them
// with OR for broader recall. FTS5 implicit AND requires all terms to appear,
// which is too restrictive for fuzzy search.
func SanitizeFTS5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return query
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		// Strip non-alphanumeric characters that break FTS5 quoting
		clean := strings.Map(func(r rune) rune {
			if r == '"' {
				return -1
			}
			return r
		}, w)
		if clean != "" {
			quoted = append(quoted, `"`+clean+`"`)
		}
	}
	return strings.Join(quoted, " OR ")
}
