package codesearch

import (
	"fmt"
	"strings"
)

const (
	// OracleSearchContextLimit bounds the search-map context supplied to Oracle.
	OracleSearchContextLimit = 8 * 1024
	oracleQueryLimit         = 512
)

// ChunkRef is the body-free metadata needed to identify a code chunk.
type ChunkRef struct {
	FilePath  string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
}

// BuildOracleQuery joins bead text into a normalized, rune-safe query.
func BuildOracleQuery(title, description, acceptance string) string {
	query := strings.Join(strings.Fields(strings.Join([]string{title, description, acceptance}, " ")), " ")
	return truncateUTF8(query, oracleQueryLimit)
}

// FormatOracleMap renders ranked chunk metadata without source bodies.
func FormatOracleMap(chunks []ChunkRef, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes > OracleSearchContextLimit {
		maxBytes = OracleSearchContextLimit
	}

	var b strings.Builder
	seen := make(map[ChunkRef]struct{}, len(chunks))
	for _, chunk := range chunks {
		if !validChunkRef(chunk) {
			continue
		}
		if _, ok := seen[chunk]; ok {
			continue
		}

		entry := fmt.Sprintf("%s:%d-%d %s %s", chunk.FilePath, chunk.StartLine, chunk.EndLine, chunk.Kind, chunk.Name)
		if b.Len() > 0 {
			entry = "\n" + entry
		}
		if b.Len()+len(entry) > maxBytes {
			break
		}
		seen[chunk] = struct{}{}
		b.WriteString(entry)
	}
	return b.String()
}

func validChunkRef(chunk ChunkRef) bool {
	return strings.TrimSpace(chunk.FilePath) != "" &&
		strings.TrimSpace(chunk.Name) != "" &&
		strings.TrimSpace(chunk.Kind) != "" &&
		chunk.StartLine > 0 && chunk.EndLine >= chunk.StartLine
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}

	end := 0
	for index := range value {
		if index > maxBytes {
			break
		}
		end = index
	}
	return value[:end]
}
