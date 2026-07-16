package processenv

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

func splitNULDelimitedEntries(contents []byte) []string {
	entries := make([]string, 0)
	for len(contents) > 0 {
		entry, rest, found := bytes.Cut(contents, []byte{'\x00'})
		if !found {
			entries = append(entries, string(entry))
			break
		}
		if len(entry) > 0 {
			entries = append(entries, string(entry))
		}
		contents = rest
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
	rest, ok := skipNULDelimitedEntry(raw[4:])
	if !ok {
		return nil, fmt.Errorf("kern.procargs2 executable path missing")
	}
	rest = bytes.TrimLeft(rest, "\x00")
	for range argc {
		rest, ok = skipNULDelimitedEntry(rest)
		if !ok {
			return nil, fmt.Errorf("kern.procargs2 argv truncated")
		}
	}
	return splitNULDelimitedEntries(bytes.TrimLeft(rest, "\x00")), nil
}

func skipNULDelimitedEntry(contents []byte) (remaining []byte, ok bool) {
	_, remaining, ok = bytes.Cut(contents, []byte{'\x00'})
	return remaining, ok
}
