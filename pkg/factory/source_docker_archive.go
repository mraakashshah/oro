package factory

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// decodeDockerSourceArchive accepts the root and single './' prefix emitted by
// docker cp container:/source/. -. Raw tar types are checked before Next hides
// any metadata; every normalized source path then uses the ordinary validator.
func decodeDockerSourceArchive(reader io.Reader, limits sourceLimits) ([]sourceEntry, error) {
	rootSeen := false
	entries, err := decodeSourceArchiveEntries(reader, limits, func(header *tar.Header) (bool, error) {
		if !rootSeen {
			if err := validateSourceMetadata(header); err != nil {
				return false, err
			}
			if header.Name != "./" || header.Typeflag != tar.TypeDir || header.Size != 0 {
				return false, errors.New("docker source archive requires one first './' directory without data")
			}
			rootSeen = true
			return true, nil
		}
		if header.Name == "./" || !strings.HasPrefix(header.Name, "./") {
			return false, errors.New("docker source archive has a repeated root or missing './' prefix")
		}
		header.Name = strings.TrimPrefix(header.Name, "./")
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if !rootSeen {
		return nil, errors.New("docker source archive is missing its root directory")
	}
	return entries, nil
}

// encodeDockerSourceArchive preflights the complete seed before resource
// allocation. Its caller must use docker cp WITHOUT -a or -L: archive mode
// rewrites ownership to the guard UID, and following links changes source bytes.
// Explicit directory headers preserve candidate ownership of implicit parents.
func encodeDockerSourceArchive(entries []sourceEntry, limits sourceLimits) ([]byte, error) {
	if !validSourceLimits(limits) {
		return nil, errors.New("docker source archive requires positive, nonoverflowing limits")
	}
	nodes, files, err := dockerSourceNodes(entries, limits)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.name)
	}
	sort.Slice(names, func(i, j int) bool {
		leftLink, rightLink := files[names[i]].Mode == 0o120000, files[names[j]].Mode == 0o120000
		if leftLink != rightLink {
			return !leftLink
		}
		return names[i] < names[j]
	})
	var out boundedDockerArchive
	out.limit = limits.MaxArchiveBytes
	stream := tar.NewWriter(&out)
	for _, name := range names {
		header := dockerSourceHeader(nodes[strings.ToLower(name)], files[name])
		if err := preflightDockerSourceHeader(header); err != nil {
			return nil, err
		}
		if err := stream.WriteHeader(&header); err != nil {
			return nil, fmt.Errorf("write Docker seed header: %w", err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := stream.Write(files[name].Data); err != nil {
				return nil, fmt.Errorf("write Docker seed bytes: %w", err)
			}
		}
	}
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("finish Docker seed archive: %w", err)
	}
	return out.Bytes(), nil
}

func dockerSourceNodes(entries []sourceEntry, limits sourceLimits) (nodes map[string]sourcePathNode, files map[string]sourceEntry, err error) {
	nodes = make(map[string]sourcePathNode)
	files = make(map[string]sourceEntry)
	var total int64
	for _, entry := range entries {
		if entry.Mode != 0o100644 && entry.Mode != 0o100755 && entry.Mode != 0o120000 {
			return nil, nil, errors.New("docker seed requires canonical Git regular-file or symlink modes")
		}
		header := dockerSourceHeader(sourcePathNode{name: entry.Path}, entry)
		if _, _, err := validateSourceHeader(&header, limits); err != nil {
			return nil, nil, err
		}
		if int64(len(entry.Data)) > limits.MaxBytes-total {
			return nil, nil, errors.New("docker seed exceeds expanded byte limit")
		}
		if err := addSourcePath(nodes, entry.Path, false, limits.MaxFiles); err != nil {
			return nil, nil, err
		}
		total += int64(len(entry.Data))
		files[entry.Path] = entry
	}
	if err := validateSourceLinks(entries); err != nil {
		return nil, nil, err
	}
	return nodes, files, nil
}

func dockerSourceHeader(node sourcePathNode, entry sourceEntry) tar.Header {
	header := tar.Header{Name: node.name, Uid: 1000, Gid: 1000, Format: tar.FormatUSTAR}
	switch {
	case node.directory:
		header.Name += "/"
		header.Typeflag, header.Mode = tar.TypeDir, 0o755
	case entry.Mode == 0o120000:
		header.Typeflag, header.Mode, header.Linkname = tar.TypeSymlink, 0o777, string(entry.Data)
	default:
		header.Typeflag, header.Mode, header.Size = tar.TypeReg, int64(entry.Mode&0o777), int64(len(entry.Data))
	}
	return header
}

func preflightDockerSourceHeader(header tar.Header) error {
	// A name that fits USTAR on input can require PAX after Docker adds './'.
	// Refuse it now, while no volume, keeper or candidate exists. The discard
	// writer checks encoding only; file data is separately bounded and written.
	header.Size = 0
	for _, name := range []string{header.Name, "./" + header.Name} {
		header.Name = name
		if err := tar.NewWriter(io.Discard).WriteHeader(&header); err != nil {
			return fmt.Errorf("docker source path cannot round-trip through USTAR: %w", err)
		}
	}
	return nil
}

type boundedDockerArchive struct {
	bytes.Buffer
	limit int64
}

func (out *boundedDockerArchive) Write(data []byte) (int, error) {
	if int64(len(data)) > out.limit-int64(out.Len()) {
		return 0, errors.New("docker seed exceeds complete archive byte limit")
	}
	n, err := out.Buffer.Write(data)
	if err != nil {
		return n, fmt.Errorf("buffer Docker seed: %w", err)
	}
	return n, nil
}
