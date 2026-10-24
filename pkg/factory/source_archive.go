package factory

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

type sourceEntry struct {
	Path string
	// Mode is a canonical Git regular-file mode, not host permission metadata.
	Mode uint32
	Data []byte
}

type sourceLimits struct {
	MaxArchiveBytes, MaxBytes, MaxFileBytes int64
	// MaxFiles also bounds unique implicit directories, limiting total inodes.
	MaxFiles, MaxPathBytes int
}

// decodeSourceArchive accepts bounded, uncompressed USTAR streams. It never
// extracts files or interprets Git attributes. Paths remain raw repository paths;
// callers must not strip a transport prefix without revalidating the result.
func decodeSourceArchive(reader io.Reader, limits sourceLimits) ([]sourceEntry, error) {
	return decodeSourceArchiveEntries(reader, limits, nil)
}

func decodeSourceArchiveEntries(reader io.Reader, limits sourceLimits, normalize func(*tar.Header) (bool, error)) ([]sourceEntry, error) {
	archive, err := readSourceArchive(reader, limits)
	if err != nil {
		return nil, err
	}
	input := bytes.NewReader(archive)
	stream := tar.NewReader(input)
	nodes := make(map[string]sourcePathNode)
	var entries []sourceEntry
	var total int64
	for {
		header, err := nextSourceArchiveHeader(archive, input, stream, normalize)
		if err != nil {
			return nil, err
		}
		if header == nil {
			return entries, nil
		}
		name, directory, err := validateSourceHeader(header, limits)
		if err != nil {
			return nil, err
		}
		if err := addSourcePath(nodes, name, directory, limits.MaxFiles); err != nil {
			return nil, err
		}
		if directory {
			continue
		}
		entry, err := readSourceEntry(stream, header, name, limits.MaxBytes-total)
		if err != nil {
			return nil, err
		}
		total += header.Size
		entries = append(entries, entry)
	}
}

func nextSourceArchiveHeader(archive []byte, input *bytes.Reader, stream *tar.Reader, normalize func(*tar.Header) (bool, error)) (*tar.Header, error) {
	for {
		before := input.Len()
		if err := rejectSourceSpecialHeader(archive, before); err != nil {
			return nil, err
		}
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return nil, validateSourceArchiveEnd(archive, input.Len(), before)
		}
		if err != nil {
			return nil, fmt.Errorf("read source archive header: %w", err)
		}
		if normalize == nil {
			return header, nil
		}
		skip, err := normalize(header)
		if err != nil {
			return nil, err
		}
		if !skip {
			return header, nil
		}
	}
}

func readSourceEntry(stream io.Reader, header *tar.Header, name string, remainingBytes int64) (sourceEntry, error) {
	if header.Size > remainingBytes {
		return sourceEntry{}, errors.New("source archive exceeds expanded byte limit")
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		return sourceEntry{}, fmt.Errorf("read source file %q: %w", name, err)
	}
	mode := uint32(0o100644)
	// Git derives its executable bit from owner permission, not group/other.
	if header.Mode&0o100 != 0 {
		mode = 0o100755
	}
	return sourceEntry{Path: name, Mode: mode, Data: data}, nil
}

func readSourceArchive(reader io.Reader, limits sourceLimits) ([]byte, error) {
	if reader == nil || !validSourceLimits(limits) {
		return nil, errors.New("source archive requires positive, nonoverflowing limits and a reader")
	}
	// Read at most one byte beyond the admitted bound, including all trailing
	// padding. Buffering before parsing also bounds hidden tar metadata records.
	archive, err := io.ReadAll(io.LimitReader(reader, limits.MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read source archive: %w", err)
	}
	if int64(len(archive)) > limits.MaxArchiveBytes {
		return nil, errors.New("source archive exceeds archive byte limit")
	}
	return archive, nil
}

func validSourceLimits(limits sourceLimits) bool {
	return limits.MaxArchiveBytes > 0 && limits.MaxArchiveBytes != math.MaxInt64 &&
		limits.MaxBytes > 0 && limits.MaxFileBytes > 0 && limits.MaxFiles > 0 && limits.MaxPathBytes > 0
}

func rejectSourceSpecialHeader(archive []byte, remaining int) error {
	offset := len(archive) - remaining
	padding := (512 - offset%512) % 512
	if remaining < padding+512 {
		return nil // The tar parser reports a truncated header or missing end.
	}
	// Next hides extended headers, even when no regular file follows. Check
	// the raw type before Next can consume any such metadata or sparse maps.
	switch archive[offset+padding+156] {
	case 0, tar.TypeReg, tar.TypeDir: // NUL is the legacy regular-file type or a zero end block.
		return nil
	default:
		return errors.New("source archive contains an unsupported raw entry type")
	}
}

func validateSourceArchiveEnd(archive []byte, remaining, before int) error {
	end := len(archive) - remaining
	// archive/tar permits a missing terminator. Require both zero blocks, then
	// only complete zero padding blocks; reject concatenated or hidden archives.
	if before-remaining < 1024 || len(archive)%512 != 0 {
		return errors.New("source archive has no complete end marker")
	}
	for _, value := range archive[end-1024:] {
		if value != 0 {
			return errors.New("source archive has nonzero trailing bytes")
		}
	}
	return nil
}

func validateSourceHeader(header *tar.Header, limits sourceLimits) (name string, directory bool, err error) {
	if err := validateSourceMetadata(header); err != nil {
		return "", false, err
	}
	directory = header.Typeflag == tar.TypeDir
	if header.Size < 0 || header.Size > limits.MaxFileBytes || (directory && header.Size != 0) {
		return "", false, fmt.Errorf("source archive entry %q exceeds file byte limit or has directory data", header.Name)
	}
	name = header.Name
	if len(name) > limits.MaxPathBytes {
		return "", false, errors.New("source archive exceeds path byte limit")
	}
	if directory {
		name = strings.TrimSuffix(name, "/")
	}
	if err := validateSourcePath(name); err != nil {
		return "", false, err
	}
	return name, directory, nil
}

func validateSourceMetadata(header *tar.Header) error {
	if header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 {
		return errors.New("source archive supports USTAR without extended metadata only")
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
		return fmt.Errorf("source archive entry %q has unsupported type %d", header.Name, header.Typeflag)
	}
	if header.Linkname != "" || header.Mode < 0 || header.Mode&^0o777 != 0 || header.Devmajor != 0 || header.Devminor != 0 {
		return fmt.Errorf("source archive entry %q has unsupported mode or link/device metadata", header.Name)
	}
	return nil
}

// Printable ASCII avoids Unicode normalization ambiguity across host and guest
// filesystems. Leading dots are valid; aliases, reserved names and separators
// that acquire different meanings on another supported filesystem are not.
func validateSourcePath(name string) error {
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasPrefix(component, " ") || strings.HasSuffix(component, " ") || strings.HasSuffix(component, ".") {
			return fmt.Errorf("source path %q has an ambiguous or nonrelative component", name)
		}
		for _, value := range component {
			if value < ' ' || value > '~' || strings.ContainsRune(`\:*?"<>|`, value) {
				return fmt.Errorf("source path %q is outside the supported printable ASCII namespace", name)
			}
		}
		lower := strings.ToLower(component)
		base, _, _ := strings.Cut(lower, ".")
		if lower == ".git" || lower == "git~1" || sourceReservedName(base) {
			return fmt.Errorf("source path %q contains a reserved component", name)
		}
	}
	return nil
}

func sourceReservedName(base string) bool {
	switch base {
	case "con", "prn", "aux", "nul", "conin$", "conout$":
		return true
	default:
		return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9'
	}
}

type sourcePathNode struct {
	name                string
	directory, explicit bool
}

func addSourcePath(nodes map[string]sourcePathNode, name string, directory bool, maxNodes int) error {
	components := strings.Split(name, "/")
	for i := range components {
		path := strings.Join(components[:i+1], "/")
		key := strings.ToLower(path)
		explicit := i == len(components)-1
		isDir := !explicit || directory
		previous, exists := nodes[key]
		if exists && (previous.name != path || !previous.directory || !isDir || (explicit && previous.explicit)) {
			return fmt.Errorf("source path %q duplicates or conflicts with %q", path, previous.name)
		}
		if !exists && len(nodes) >= maxNodes {
			return errors.New("source archive exceeds file and directory count limit")
		}
		nodes[key] = sourcePathNode{name: path, directory: isDir, explicit: explicit || previous.explicit}
	}
	return nil
}
