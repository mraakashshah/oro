// Package memoryeval validates content-word overlap for paraphrase quality.
package memoryeval

import (
	"strings"
	"unicode"
)

// MaxSharedContentWords is the maximum number of shared content words
// allowed between a paraphrase and its source anchor before the
// paraphrase is rejected as too lexically similar.
const MaxSharedContentWords = 3

// builtinStopwordList is the default English stop-word vocabulary,
// one word per token, space-separated. Applied when no external
// stopwords file is provided.
const builtinStopwordList = `a about above after against all am an and any are as at be been ` +
	`being below between both but by can could did do does doing down during each ` +
	`few for from further had has have having he her here hers herself him himself ` +
	`his how i if in into is it its itself just me may might more most must my ` +
	`myself no nor not of off on once only or other our ours ourselves out over own ` +
	`same she should so some such than that the their theirs them themselves then ` +
	`there these they this those through to too under until up very was we were ` +
	`what when where which while who whom why will with would you your yours ` +
	`yourself yourselves`

// CountSharedContentWords returns the count of lemmatized content words
// shared between strings a and b. Words are case-folded, stop-words are
// excluded using the built-in English stop-word list, and a plural-s-only
// lemmatizer is applied (workers→worker, crashes→crash). Verb inflections
// (ran, running) are not normalized — documented limitation.
// Returns 0 for empty input.
func CountSharedContentWords(a, b string) int {
	stops := defaultStopwords()
	wordsA := contentWordSet(a, stops)
	wordsB := contentWordSet(b, stops)
	count := 0
	for w := range wordsA {
		if wordsB[w] {
			count++
		}
	}
	return count
}

// contentWordSet returns the set of lemmatized content words in s,
// excluding words present in stops.
func contentWordSet(s string, stops map[string]bool) map[string]bool {
	tokens := tokenizeWords(s)
	result := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		if !stops[tok] {
			result[lemmatizePlurals(tok)] = true
		}
	}
	return result
}

// tokenizeWords splits s into lowercase alphanumeric tokens.
func tokenizeWords(s string) []string {
	lower := strings.ToLower(s)
	tokens := make([]string, 0, len(lower)/5+1)
	cur := make([]rune, 0, 32)
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	if len(cur) > 0 {
		tokens = append(tokens, string(cur))
	}
	return tokens
}

// lemmatizePlurals strips a trailing plural-s from w, handling:
//   - words ending in "es" of length ≥ 5 (crashes→crash, classes→class)
//   - words ending in "s" but not "ss" of length ≥ 3 (workers→worker)
//
// Verb inflections (ran, running) are not handled.
func lemmatizePlurals(w string) string {
	if len(w) >= 5 && strings.HasSuffix(w, "es") {
		return w[:len(w)-2]
	}
	if len(w) >= 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return w[:len(w)-1]
	}
	return w
}

// defaultStopwords returns the built-in English stop-word set.
func defaultStopwords() map[string]bool {
	words := strings.Fields(builtinStopwordList)
	stops := make(map[string]bool, len(words))
	for _, w := range words {
		stops[w] = true
	}
	return stops
}
