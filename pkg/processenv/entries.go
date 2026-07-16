package processenv

import (
	"encoding/binary"
	"fmt"
	"strings"
)

func splitNULDelimitedEntries(contents []byte) []string {
	entries := strings.Split(strings.TrimSuffix(string(contents), "\x00"), "\x00")
	if len(entries) == 1 && entries[0] == "" {
		return nil
	}
	return entries
}

// ParseDarwinEntries decodes the NUL-delimited environment suffix of a
// kern.procargs2 payload without treating whitespace as an entry separator.
func ParseDarwinEntries(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("kern.procargs2 payload too short")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	rest, ok := skipNULDelimitedEntry(rest)
	if !ok {
		return nil, fmt.Errorf("kern.procargs2 executable path missing")
	}
	rest = bytesAfterNULPadding(rest)
	for range argc {
		rest, ok = skipNULDelimitedEntry(rest)
		if !ok {
			return nil, fmt.Errorf("kern.procargs2 argv truncated")
		}
	}
	rest = bytesAfterNULPadding(rest)
	return splitNULDelimitedEntries(rest), nil
}

func skipNULDelimitedEntry(contents []byte) (remaining []byte, ok bool) {
	end := strings.IndexByte(string(contents), 0)
	if end < 0 {
		return nil, false
	}
	return contents[end+1:], true
}

func bytesAfterNULPadding(contents []byte) []byte {
	return contents[len(contents)-len(strings.TrimLeft(string(contents), "\x00")):]
}
