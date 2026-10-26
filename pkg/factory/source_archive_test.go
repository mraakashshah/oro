package factory //nolint:testpackage // Exercises the private, pure candidate archive boundary.

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSourceArchiveInternalLinks(t *testing.T) {
	t.Run("Oro raw targets are order independent", testSourceLinksOro)
	t.Run("raw target quotas", testSourceLinksQuotas)
	t.Run("traversal and target rejection", testSourceLinksRejected)
	t.Run("pure vector target grammar", testSourceLinkVectorGrammar)
	t.Run("valid canceled directory traversal", func(t *testing.T) {
		archive := candidateArchive(t, sourceLinkFixture("link", "existing-dir/../file"), sourceLinkFileFixture("file"), sourceLinkFileFixture("existing-dir/child"))
		got, err := decodeSourceArchive(bytes.NewReader(archive), sourceLinkLimits())
		if err != nil || len(got) != 3 || string(got[0].Data) != "existing-dir/../file" {
			t.Fatalf("valid raw traversal changed: %+v, %v", got, err)
		}
	})
	t.Run("100 byte target", func(t *testing.T) {
		target := strings.Repeat("a", 100)
		archive := candidateArchive(t, sourceLinkFixture("link", target), sourceLinkFileFixture(target))
		got, err := decodeSourceArchive(bytes.NewReader(archive), sourceLinkLimits())
		if err != nil || len(got) != 2 || got[0].Mode != 0o120000 || string(got[0].Data) != target {
			t.Fatalf("exact target limit rejected: %+v, %v", got, err)
		}
	})
	t.Run("nonzero symlink size", func(t *testing.T) {
		archive := candidateArchive(t, sourceLinkFixture("link", "file"), sourceLinkFileFixture("file"))
		copy(archive[124:136], "00000000001\x00")
		copy(archive[148:156], "        ")
		var checksum int
		for _, value := range archive[:512] {
			checksum += int(value)
		}
		copy(archive[148:156], fmt.Sprintf("%06o\x00 ", checksum))
		assertCandidateArchiveRejected(t, archive, sourceLinkLimits())
	})
}

func sourceLinkLimits() sourceLimits {
	return sourceLimits{MaxArchiveBytes: 1 << 20, MaxBytes: 1024, MaxFileBytes: 512, MaxFiles: 32, MaxPathBytes: 256}
}

func sourceLinkFixture(name, target string) archiveFixtureEntry {
	return archiveFixtureEntry{header: tar.Header{Name: name, Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: target}}
}

func sourceLinkFileFixture(name string) archiveFixtureEntry {
	return archiveFixtureEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, data: "x"}
}

func testSourceLinksOro(t *testing.T) {
	t.Helper()
	files := []archiveFixtureEntry{
		sourceLinkFixture(".claude/hooks/context_block_stop.py", "../../assets/hooks/context_block_stop.py"),
		sourceLinkFixture(".claude/hooks/context_pct_writer.py", "../../assets/hooks/context_pct_writer.py"),
		sourceLinkFileFixture("assets/hooks/context_block_stop.py"),
		sourceLinkFileFixture("assets/hooks/context_pct_writer.py"),
	}
	files[3].header.Mode = 0o755
	want := make(map[string]sourceEntry)
	for _, entry := range files {
		mode, data := uint32(0o100644), entry.data
		if entry.header.Typeflag == tar.TypeSymlink {
			mode, data = 0o120000, entry.header.Linkname
		} else if entry.header.Mode == 0o755 {
			mode = 0o100755
		}
		want[entry.header.Name] = sourceEntry{Path: entry.header.Name, Mode: mode, Data: []byte(data)}
	}
	for _, order := range [][]archiveFixtureEntry{files, {files[3], files[2], files[1], files[0]}} {
		got, err := decodeSourceArchive(bytes.NewReader(candidateArchive(t, order...)), sourceLinkLimits())
		if err != nil || len(got) != len(want) {
			t.Fatalf("decode Oro links: %+v, %v", got, err)
		}
		for _, entry := range got {
			if !reflect.DeepEqual(entry, want[entry.Path]) {
				t.Fatalf("link target or mode changed: %+v", entry)
			}
		}
	}
}

func testSourceLinksQuotas(t *testing.T) {
	t.Helper()
	link, file := sourceLinkFixture("d/link", "../file"), sourceLinkFileFixture("file")
	for _, order := range [][]archiveFixtureEntry{{link, file}, {file, link}} {
		archive := candidateArchive(t, order...)
		limits := sourceLimits{MaxArchiveBytes: int64(len(archive)), MaxBytes: 8, MaxFileBytes: 7, MaxFiles: 3, MaxPathBytes: 6}
		if got, err := decodeSourceArchive(bytes.NewReader(archive), limits); err != nil || len(got) != 2 {
			t.Fatalf("exact link quotas rejected: %+v, %v", got, err)
		}
		for _, mutate := range []func(*sourceLimits){
			func(l *sourceLimits) { l.MaxBytes-- },
			func(l *sourceLimits) { l.MaxFileBytes-- },
			func(l *sourceLimits) { l.MaxFiles-- },
		} {
			bounded := limits
			mutate(&bounded)
			assertCandidateArchiveRejected(t, archive, bounded)
		}
	}
}

func testSourceLinksRejected(t *testing.T) {
	t.Helper()
	for _, target := range []string{
		"", "..", "../../outside", "/file", "//file", "C:/file", `dir\file`, "./file", "existing-dir//child",
		".git/../file", "git~1/../file", "CON/../file", "file.", " file", "file ", "missing", "link", "existing-dir", "FILE",
		"nonexistent/../file", "regular/../file", "link/../file", "EXISTING-dir/../file",
	} {
		t.Run(target, func(t *testing.T) {
			archive := candidateArchive(t, sourceLinkFixture("subject", target), sourceLinkFileFixture("file"),
				sourceLinkFileFixture("existing-dir/child"), sourceLinkFileFixture("regular"), sourceLinkFixture("link", "file"))
			assertCandidateArchiveRejected(t, archive, sourceLinkLimits())
		})
	}
	for name, files := range map[string][]archiveFixtureEntry{
		"cycle":              {sourceLinkFixture("one", "two"), sourceLinkFixture("two", "one")},
		"self":               {sourceLinkFixture("self", "self")},
		"symlink parent":     {sourceLinkFixture("link", "file"), sourceLinkFileFixture("link/child"), sourceLinkFileFixture("file")},
		"parent after child": {sourceLinkFileFixture("link/child"), sourceLinkFixture("link", "file"), sourceLinkFileFixture("file")},
		// An empty tar-only directory has no Git leaf and cannot survive export.
		"empty directory traversal": {
			sourceLinkFixture("link", "empty/../file"), sourceLinkFileFixture("file"),
			{header: tar.Header{Name: "empty/", Mode: 0o755, Typeflag: tar.TypeDir}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertCandidateArchiveRejected(t, candidateArchive(t, files...), sourceLinkLimits())
		})
	}
	for _, target := range []string{strings.Repeat("a", 101), "nonascii-é"} {
		link := sourceLinkFixture("link", target)
		link.header.Format = tar.FormatPAX
		assertCandidateArchiveRejected(t, candidateArchive(t, link), sourceLinkLimits())
	}
}

func testSourceLinkVectorGrammar(t *testing.T) {
	t.Helper()
	file := sourceEntry{Path: "file", Mode: 0o100644, Data: []byte("x")}
	for _, target := range []string{"file\x00", "fi\nle", "fi\x7fle", "nonascii-é", strings.Repeat("a", 101)} {
		entries := []sourceEntry{file, {Path: "link", Mode: 0o120000, Data: []byte(target)}}
		if len(target) == 101 {
			// A matching endpoint must not conceal the raw target length error.
			entries[0].Path = target
		}
		if err := validateSourceLinks(entries); err == nil {
			t.Fatalf("unsupported raw target accepted: %q", target)
		}
	}
	for name, entries := range map[string][]sourceEntry{
		"unsupported mode": {file, {Path: "submodule", Mode: 0o160000}},
		"invalid path":     {file, {Path: "../link", Mode: 0o120000, Data: []byte("file")}},
		"case collision":   {file, {Path: "FILE", Mode: 0o100644}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSourceLinks(entries); err == nil {
				t.Fatal("invalid source vector accepted")
			}
		})
	}
}

func TestCandidateImportRejectsEscapes(t *testing.T) {
	limits := sourceLimits{MaxArchiveBytes: 1 << 20, MaxBytes: 1024, MaxFileBytes: 512, MaxFiles: 16, MaxPathBytes: 256}
	regular := func(path string) archiveFixtureEntry {
		return archiveFixtureEntry{header: tar.Header{Name: path, Mode: 0o644, Typeflag: tar.TypeReg}, data: "source"}
	}
	t.Run("regular bytes and executable mode", func(t *testing.T) {
		files := []archiveFixtureEntry{
			{header: tar.Header{Name: ".github/", Mode: 0o755, Typeflag: tar.TypeDir}},
			{header: tar.Header{Name: ".github/config", Mode: 0o640, Typeflag: tar.TypeReg}, data: "\x00\xffexact\n"},
			{header: tar.Header{Name: "docs/decisions&discoveries.md", Mode: 0o644, Typeflag: tar.TypeReg}, data: "unchanged"},
			{header: tar.Header{Name: "bin/run script", Mode: 0o755, Typeflag: tar.TypeReg}, data: "#!/bin/sh\n"},
		}
		got, err := decodeSourceArchive(bytes.NewReader(candidateArchive(t, files...)), limits)
		want := []sourceEntry{
			{Path: ".github/config", Mode: 0o100644, Data: []byte("\x00\xffexact\n")},
			{Path: "docs/decisions&discoveries.md", Mode: 0o100644, Data: []byte("unchanged")},
			{Path: "bin/run script", Mode: 0o100755, Data: []byte("#!/bin/sh\n")},
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("decode = %#v, %v; want %#v", got, err, want)
		}
	})
	t.Run("Git executable mode follows owner permission", func(t *testing.T) {
		for _, test := range []struct {
			name string
			mode int64
			want uint32
		}{
			{"other executable only", 0o641, 0o100644},
			{"group executable only", 0o654, 0o100644},
			{"owner executable only", 0o744, 0o100755},
			{"all executable", 0o755, 0o100755},
			{"none executable", 0o644, 0o100644},
		} {
			t.Run(test.name, func(t *testing.T) {
				entry := regular("file")
				entry.header.Mode = test.mode
				got, err := decodeSourceArchive(bytes.NewReader(candidateArchive(t, entry)), limits)
				if err != nil || len(got) != 1 {
					t.Fatalf("decode mode %o: %+v, %v", test.mode, got, err)
				}
				if got[0].Mode != test.want {
					t.Errorf("archive mode %o became Git mode %o, want %o", test.mode, got[0].Mode, test.want)
				}
			})
		}
	})
	for _, path := range []string{
		"../escape", "dir/../../escape", "/absolute", "//server/share", "./file", "dir/./file", "dir//file",
		".", "..", ".git/config", "dir/.GiT/config", "dir/../file", "dir\\file", "C:/file",
		"name.", "name ", " leading", "dir/CON.txt", "nul", "AUX", "com1.log", "LPT9",
		"name:stream", "name?", "line\nbreak", "tab\tname", "nonascii-é",
	} {
		t.Run("path "+path, func(t *testing.T) {
			entry := regular(path)
			if strings.Contains(path, "é") {
				entry.header.Format = tar.FormatPAX
			}
			assertCandidateArchiveRejected(t, candidateArchive(t, entry), limits)
		})
	}
	for _, paths := range [][]string{
		{"same", "same"},
		{"Case", "case"},
		{"Dir/one", "dir/two"},
		{"file", "file/child"},
		{"file/child", "file"},
	} {
		t.Run("conflict "+strings.Join(paths, ","), func(t *testing.T) {
			assertCandidateArchiveRejected(t, candidateArchive(t, regular(paths[0]), regular(paths[1])), limits)
		})
	}
	for name, kind := range map[string]byte{
		"symlink": tar.TypeSymlink, "hardlink": tar.TypeLink, "block device": tar.TypeBlock,
		"character device": tar.TypeChar, "FIFO": tar.TypeFifo, "unsupported": 'Z',
	} {
		t.Run(name, func(t *testing.T) {
			entry := archiveFixtureEntry{header: tar.Header{Name: "unsafe", Mode: 0o644, Typeflag: kind, Linkname: "../outside"}}
			assertCandidateArchiveRejected(t, candidateArchive(t, entry), limits)
		})
	}
	for name, mode := range map[string]int64{"submodule": 0o160000, "setuid": 0o4644, "negative mode": -1} {
		t.Run(name, func(t *testing.T) {
			entry := regular("unsafe")
			entry.header.Mode = mode
			if mode < 0 {
				entry.header.Format = tar.FormatGNU
			}
			assertCandidateArchiveRejected(t, candidateArchive(t, entry), limits)
		})
	}
	t.Run("duplicate explicit directory", func(t *testing.T) {
		dir := archiveFixtureEntry{header: tar.Header{Name: "dir/", Mode: 0o755, Typeflag: tar.TypeDir}}
		assertCandidateArchiveRejected(t, candidateArchive(t, dir, dir), limits)
	})
	t.Run("implicit directory can be declared once", func(t *testing.T) {
		dir := archiveFixtureEntry{header: tar.Header{Name: "dir/", Mode: 0o755, Typeflag: tar.TypeDir}}
		got, err := decodeSourceArchive(bytes.NewReader(candidateArchive(t, regular("dir/file"), dir)), limits)
		if err != nil || len(got) != 1 || got[0].Path != "dir/file" {
			t.Fatalf("decode implicit parent: %+v, %v", got, err)
		}
	})
	for name, mutate := range map[string]func(*sourceLimits){
		"archive bytes":       func(l *sourceLimits) { l.MaxArchiveBytes = 1024 },
		"expanded bytes":      func(l *sourceLimits) { l.MaxBytes = 5 },
		"file bytes":          func(l *sourceLimits) { l.MaxFileBytes = 5 },
		"path bytes":          func(l *sourceLimits) { l.MaxPathBytes = 3 },
		"file count":          func(l *sourceLimits) { l.MaxFiles = 1 },
		"zero archive limit":  func(l *sourceLimits) { l.MaxArchiveBytes = 0 },
		"archive overflow":    func(l *sourceLimits) { l.MaxArchiveBytes = math.MaxInt64 },
		"negative byte limit": func(l *sourceLimits) { l.MaxBytes = -1 },
		"zero file bytes":     func(l *sourceLimits) { l.MaxFileBytes = 0 },
		"negative count":      func(l *sourceLimits) { l.MaxFiles = -1 },
		"zero path limit":     func(l *sourceLimits) { l.MaxPathBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bounded := limits
			mutate(&bounded)
			assertCandidateArchiveRejected(t, candidateArchive(t, regular("first"), regular("second")), bounded)
		})
	}
	t.Run("implicit directories consume inode limit", func(t *testing.T) {
		bounded := limits
		bounded.MaxFiles = 2
		assertCandidateArchiveRejected(t, candidateArchive(t, regular("one/two/file")), bounded)
	})
	t.Run("exact limits", func(t *testing.T) {
		archive := candidateArchive(t, regular("a"), regular("b"))
		bounded := sourceLimits{MaxArchiveBytes: int64(len(archive)), MaxBytes: 12, MaxFileBytes: 6, MaxFiles: 2, MaxPathBytes: 1}
		if got, err := decodeSourceArchive(bytes.NewReader(archive), bounded); err != nil || len(got) != 2 {
			t.Fatalf("exact limits rejected: %+v, %v", got, err)
		}
	})
	t.Run("trailing bytes count against archive limit", func(t *testing.T) {
		archive := candidateArchive(t, regular("file"))
		bounded := limits
		bounded.MaxArchiveBytes = int64(len(archive))
		reader := &countingArchiveReader{Reader: io.MultiReader(bytes.NewReader(archive), io.LimitReader(zeroArchiveReader{}, 1<<30))}
		if got, err := decodeSourceArchive(reader, bounded); err == nil || got != nil {
			t.Fatalf("oversized trailer accepted: %+v, %v", got, err)
		}
		if reader.read > bounded.MaxArchiveBytes+1 {
			t.Fatalf("read %d bytes beyond archive bound %d", reader.read, bounded.MaxArchiveBytes)
		}
	})
	t.Run("malformed and trailing content", func(t *testing.T) {
		archive := candidateArchive(t, regular("file"))
		for _, malformed := range [][]byte{nil, archive[:512], archive[:1024], append(append([]byte(nil), archive...), 'x'), append(append([]byte(nil), archive...), archive...)} {
			assertCandidateArchiveRejected(t, malformed, limits)
		}
	})
	t.Run("metadata header without a file", func(t *testing.T) {
		entry := regular("nonascii-é")
		entry.header.Format = tar.FormatPAX
		archive := candidateArchive(t, entry)
		if archive[156] != tar.TypeXHeader {
			t.Fatal("fixture did not produce an extended metadata header")
		}
		// Keep the metadata header and its one data block, followed by the
		// terminator. archive/tar hides this header even when no file follows.
		metadataOnly := append(append([]byte(nil), archive[:1024]...), make([]byte, 1024)...)
		assertCandidateArchiveRejected(t, metadataOnly, limits)
	})
	t.Run("reader error", func(t *testing.T) {
		if got, err := decodeSourceArchive(failingArchiveReader{}, limits); err == nil || got != nil {
			t.Fatalf("reader failure accepted: %+v, %v", got, err)
		}
	})
}

type archiveFixtureEntry struct {
	header tar.Header
	data   string
}

func candidateArchive(t *testing.T, entries ...archiveFixtureEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, entry := range entries {
		header := entry.header
		if header.Format == tar.FormatUnknown {
			header.Format = tar.FormatUSTAR
		}
		header.Size = int64(len(entry.data))
		if err := w.WriteHeader(&header); err != nil {
			t.Fatalf("write fixture header: %v", err)
		}
		if _, err := io.WriteString(w, entry.data); err != nil {
			t.Fatalf("write fixture content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fixture archive: %v", err)
	}
	return out.Bytes()
}

func assertCandidateArchiveRejected(t *testing.T, archive []byte, limits sourceLimits) {
	t.Helper()
	if got, err := decodeSourceArchive(bytes.NewReader(archive), limits); err == nil || got != nil {
		t.Fatalf("unsafe archive accepted: %+v, error %v", got, err)
	}
}

type countingArchiveReader struct {
	io.Reader
	read int64
}

func (r *countingArchiveReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	return n, err
}

type zeroArchiveReader struct{}

func (zeroArchiveReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type failingArchiveReader struct{}

func (failingArchiveReader) Read([]byte) (int, error) { return 0, errors.New("broken archive stream") }
