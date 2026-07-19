package codesearch

import (
	"fmt"
	"os"
	"path/filepath"
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

// FilterOracleChunksForWorktree retains only chunks that name regular files
// contained by worktree after symlinks are resolved. It preserves rank order.
//
//oro:testonly
func FilterOracleChunksForWorktree(worktree string, chunks []ChunkRef) []ChunkRef {
	root, ok := resolveOracleWorktree(worktree)
	if !ok {
		return nil
	}

	filtered := make([]ChunkRef, 0, len(chunks))
	for _, chunk := range chunks {
		if oracleChunkInWorktree(root, chunk.FilePath) {
			filtered = append(filtered, chunk)
		}
	}
	return filtered
}

func resolveOracleWorktree(worktree string) (string, bool) {
	if strings.TrimSpace(worktree) == "" {
		return "", false
	}

	absRoot, err := filepath.Abs(worktree)
	if err != nil {
		return "", false
	}
	root, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return root, true
}

func oracleChunkInWorktree(root, filePath string) bool {
	if filepath.IsAbs(filePath) || pathHasTraversal(filePath) {
		return false
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filePath))
	if err != nil || !pathContainedBy(root, resolved) {
		return false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func pathHasTraversal(path string) bool {
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool {
		return r == filepath.Separator || r == '/'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}

func pathContainedBy(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// BuildOracleQuery joins bead text into a normalized, rune-safe query.
//
//oro:testonly
func BuildOracleQuery(title, description, acceptance string) string {
	query := strings.Join(strings.Fields(strings.Join([]string{title, description, acceptance}, " ")), " ")
	return truncateUTF8(query, oracleQueryLimit)
}

// FormatOracleMap renders ranked chunk metadata without source bodies.
//
//oro:testonly
func FormatOracleMap(chunks []ChunkRef, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes > OracleSearchContextLimit {
		maxBytes = OracleSearchContextLimit
	}

	var b strings.Builder
	// Do not reserve capacity from chunks: callers may provide an unbounded ranked
	// result set while this renderer is explicitly bounded by maxBytes.
	seen := make(map[ChunkRef]struct{})
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
