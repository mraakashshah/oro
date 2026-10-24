package factory

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity compatibility, not authentication.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"slices"
	"strconv"
	"strings"
)

func sourceOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, digit := range value {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

func parseSourceOIDLine(data []byte) (string, error) {
	value := strings.TrimSuffix(string(data), "\n")
	if !sourceOID(value) || len(data) != len(value)+1 {
		return "", errors.New("raw Git did not return one exact object ID")
	}
	return value, nil
}

func sourceObjectID(kind string, data []byte, width int) string {
	var digest hash.Hash
	if width == 40 {
		digest = sha1.New() //nolint:gosec // Required Git object identifier, not authority or artifact authentication.
	} else {
		digest = sha256.New()
	}
	_, _ = fmt.Fprintf(digest, "%s %d\x00", kind, len(data))
	_, _ = digest.Write(data)
	return hex.EncodeToString(digest.Sum(nil))
}

func decodeSourceBatch(data []byte, oids []string, kind string, allowance int64) ([][]byte, error) {
	objects := make([][]byte, 0, len(oids))
	for _, oid := range oids {
		end := bytes.IndexByte(data, '\n')
		if end < 0 || end > 128 {
			return nil, errors.New("raw Git object batch has an invalid header")
		}
		fields := strings.Split(string(data[:end]), " ")
		if len(fields) != 3 || fields[0] != oid || fields[1] != kind {
			return nil, errors.New("raw Git object batch differs from the requested object identity or type")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > allowance || strconv.FormatInt(size, 10) != fields[2] {
			return nil, errors.New("raw Git object batch exceeds its exact byte allowance")
		}
		data = data[end+1:]
		if size >= int64(len(data)) || data[size] != '\n' {
			return nil, errors.New("raw Git object batch is truncated or incorrectly delimited")
		}
		body := data[:size]
		if sourceObjectID(kind, body, len(oid)) != oid {
			return nil, errors.New("raw Git object bytes do not match the selected object ID")
		}
		objects = append(objects, body)
		data, allowance = data[size+1:], allowance-size
	}
	if len(data) != 0 {
		return nil, errors.New("raw Git object batch has trailing data")
	}
	return objects, nil
}

type sourceTree struct {
	entries []sourceEntry
	blobs   []string
	oid     string
	input   []byte // child-first mktree --batch input with precomputed child IDs
	trees   []string
}

func buildSourceTree(entries []sourceEntry, limits sourceLimits, width int) (sourceTree, error) {
	if len(entries) > limits.MaxFiles {
		return sourceTree{}, errors.New("source entry vector exceeds its admitted count")
	}
	result := sourceTree{entries: append([]sourceEntry(nil), entries...)}
	nodes := make(map[string]sourcePathNode)
	var total int64
	for _, entry := range result.entries {
		if len(entry.Path) > limits.MaxPathBytes || int64(len(entry.Data)) > limits.MaxFileBytes || int64(len(entry.Data)) > limits.MaxBytes-total {
			return result, errors.New("source entry exceeds admitted path or byte bounds")
		}
		if err := validateSourcePath(entry.Path); err != nil {
			return result, err
		}
		if err := addSourcePath(nodes, entry.Path, false, limits.MaxFiles); err != nil {
			return result, err
		}
		total += int64(len(entry.Data))
	}
	// The shared vector validator enforces canonical leaf modes and link targets.
	if err := validateSourceLinks(result.entries); err != nil {
		return result, err
	}
	slices.SortFunc(result.entries, func(a, b sourceEntry) int { return strings.Compare(a.Path, b.Path) })
	root := &sourceGitDirectory{children: make(map[string]*sourceGitDirectory)}
	for _, entry := range result.entries {
		oid := sourceObjectID("blob", entry.Data, width)
		result.blobs = append(result.blobs, oid)
		parts := strings.Split(entry.Path, "/")
		dir := root
		for _, name := range parts[:len(parts)-1] {
			if dir.children[name] == nil {
				dir.children[name] = &sourceGitDirectory{children: make(map[string]*sourceGitDirectory)}
			}
			dir = dir.children[name]
		}
		dir.files = append(dir.files, sourceGitTreeItem{name: parts[len(parts)-1], mode: strconv.FormatUint(uint64(entry.Mode), 8), oid: oid})
	}
	result.oid = root.encode(width, &result)
	if int64(len(result.input)) > limits.MaxArchiveBytes {
		return result, errors.New("source tree metadata exceeds admitted transport bound")
	}
	return result, nil
}

type (
	sourceGitTreeItem  struct{ name, mode, oid string }
	sourceGitDirectory struct {
		children map[string]*sourceGitDirectory
		files    []sourceGitTreeItem
	}
)

func (dir *sourceGitDirectory) encode(width int, result *sourceTree) string {
	items := append([]sourceGitTreeItem(nil), dir.files...)
	children := make([]string, 0, len(dir.children))
	for name := range dir.children {
		children = append(children, name)
	}
	slices.Sort(children)
	for _, name := range children {
		items = append(items, sourceGitTreeItem{name: name, mode: "40000", oid: dir.children[name].encode(width, result)})
	}
	// Git compares directory names with a trailing '/', unlike plain lexical
	// sorting when a file and directory share a name prefix.
	slices.SortFunc(items, func(a, b sourceGitTreeItem) int {
		left, right := a.name, b.name
		if a.mode == "40000" {
			left += "/"
		}
		if b.mode == "40000" {
			right += "/"
		}
		return strings.Compare(left, right)
	})
	var raw bytes.Buffer
	for _, item := range items {
		_, _ = fmt.Fprintf(&raw, "%s %s\x00", item.mode, item.name)
		binary, _ := hex.DecodeString(item.oid)
		_, _ = raw.Write(binary)
		kind := "blob"
		if item.mode == "40000" {
			kind = "tree"
		}
		result.input = fmt.Appendf(result.input, "%s %s %s\t%s\x00", item.mode, kind, item.oid, item.name)
	}
	result.input = append(result.input, 0)
	oid := sourceObjectID("tree", raw.Bytes(), width)
	result.trees = append(result.trees, oid)
	return oid
}
