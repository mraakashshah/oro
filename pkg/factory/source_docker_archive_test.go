package factory //nolint:testpackage // Tests the private trusted Docker tar boundary without host extraction.

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDockerSourceArchive(t *testing.T) {
	t.Run("real Docker USTAR shape", testDockerArchiveShape)
	t.Run("root prefix is exact and singular", testDockerArchiveRoot)
	t.Run("raw metadata cannot be normalized away", testDockerArchiveMetadata)
	t.Run("normalized paths retain original defenses", testDockerArchivePaths)
	t.Run("bounded complete stream", testDockerArchiveBounds)
	t.Run("trusted seed ownership and execute bits", testDockerArchiveEncode)
	t.Run("seed preflights both transport names", testDockerArchiveEncodeLimits)
	t.Run("links preserve raw targets after regular entries", testDockerArchiveLinks)
	t.Run("link targets count toward every source quota", testDockerArchiveLinkQuotas)
	t.Run("unsafe complete link vectors emit nothing", testDockerArchiveLinkVectors)
	t.Run("link metadata and bodies remain strict", testDockerArchiveLinkMetadata)
	t.Run("link USTAR fields preflight both transport paths", testDockerArchiveLinkUSTAR)
}

func dockerArchiveLimits() sourceLimits {
	return sourceLimits{MaxArchiveBytes: 1 << 20, MaxBytes: 4096, MaxFileBytes: 2048, MaxFiles: 32, MaxPathBytes: 255}
}

func dockerArchiveRoot() archiveFixtureEntry {
	return archiveFixtureEntry{header: tar.Header{Name: "./", Mode: 0o700, Uid: 1000, Gid: 1000, Typeflag: tar.TypeDir}}
}

func dockerArchiveFile(name, data string) archiveFixtureEntry {
	return archiveFixtureEntry{header: tar.Header{Name: name, Mode: 0o644, Uid: 1000, Gid: 1000, Typeflag: tar.TypeReg}, data: data}
}

func testDockerArchiveShape(t *testing.T) {
	t.Helper()
	// Synthesizes the exact observed Docker29.5.2 root/prefix/USTAR encoding;
	// the live probe also proved UID1000 and execute mode survive cp without -a.
	dir := strings.Repeat("d", 80)
	long := dir + "/" + strings.Repeat("n", 80) + ".txt"
	run := dockerArchiveFile("./run.sh", "#!/bin/sh\n")
	run.header.Mode = 0o755
	archive := candidateArchive(t, dockerArchiveRoot(), dockerArchiveFile("./short", "raw\x00\xff\n"),
		archiveFixtureEntry{header: tar.Header{Name: "./" + dir + "/", Mode: 0o755, Typeflag: tar.TypeDir}}, dockerArchiveFile("./"+long, "long"), run)
	want := []sourceEntry{{Path: "short", Mode: 0o100644, Data: []byte("raw\x00\xff\n")}, {Path: long, Mode: 0o100644, Data: []byte("long")}, {Path: "run.sh", Mode: 0o100755, Data: []byte("#!/bin/sh\n")}}
	got, err := decodeDockerSourceArchive(bytes.NewReader(archive), dockerArchiveLimits())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker source decode=%#v, %v", got, err)
	}
	if _, err := decodeSourceArchive(bytes.NewReader(archive), dockerArchiveLimits()); err == nil {
		t.Fatal("ordinary decoder accepted Docker root aliases")
	}
	if got, err := decodeDockerSourceArchive(bytes.NewReader(candidateArchive(t, dockerArchiveRoot())), dockerArchiveLimits()); err != nil || len(got) != 0 {
		t.Fatalf("empty Docker source=%v, %v", got, err)
	}
}

func testDockerArchiveRoot(t *testing.T) {
	t.Helper()
	root, file := dockerArchiveRoot(), dockerArchiveFile("./file", "a")
	for _, entries := range [][]archiveFixtureEntry{nil, {file}, {file, root}, {root, root}, {root, dockerArchiveFile("file", "a")}, {root, dockerArchiveFile("././file", "a")}} {
		assertDockerArchiveRejected(t, candidateArchive(t, entries...), dockerArchiveLimits())
	}
	for _, name := range []string{".", "/", ".//", "source/", "../", "././"} {
		changed := root
		changed.header.Name = name
		assertDockerArchiveRejected(t, candidateArchive(t, changed, file), dockerArchiveLimits())
	}
	for _, change := range []func(*archiveFixtureEntry){
		func(e *archiveFixtureEntry) { e.header.Mode = 0o4700 },
		func(e *archiveFixtureEntry) { e.header.Typeflag = tar.TypeSymlink; e.header.Linkname = "outside" },
	} {
		changed := root
		change(&changed)
		assertDockerArchiveRejected(t, candidateArchive(t, changed, file), dockerArchiveLimits())
	}
	// The tar writer refuses these malformed roots, so mutate the raw header
	// and recompute its checksum to test the semantic boundary itself.
	for _, change := range []func([]byte){
		func(header []byte) { header[156] = tar.TypeReg },
		func(header []byte) { copy(header[124:136], []byte("00000000001\x00")) },
	} {
		archive := candidateArchive(t, root, file)
		change(archive[:512])
		fixDockerArchiveChecksum(archive[:512])
		assertDockerArchiveRejected(t, archive, dockerArchiveLimits())
	}
}

func fixDockerArchiveChecksum(header []byte) {
	copy(header[148:156], []byte("        "))
	var sum int
	for _, value := range header {
		sum += int(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

func testDockerArchiveMetadata(t *testing.T) {
	t.Helper()
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		entry := dockerArchiveFile("./unsafe", "")
		entry.header.Typeflag = kind
		if kind == tar.TypeSymlink || kind == tar.TypeLink {
			entry.header.Linkname = "./file"
		}
		assertDockerArchiveRejected(t, candidateArchive(t, dockerArchiveRoot(), entry), dockerArchiveLimits())
	}
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		entry := dockerArchiveFile("./"+strings.Repeat("z", 101), "long")
		entry.header.Format = format
		archive := candidateArchive(t, dockerArchiveRoot(), entry)
		if archive[512+156] == tar.TypeReg {
			t.Fatal("fixture lacks a hidden metadata header")
		}
		assertDockerArchiveRejected(t, archive, dockerArchiveLimits())
		// A raw extended header without its following file is hidden by Next.
		metadataOnly := append(append([]byte(nil), archive[:1536]...), make([]byte, 1024)...)
		assertDockerArchiveRejected(t, metadataOnly, dockerArchiveLimits())
	}
	root := dockerArchiveRoot()
	root.header.Format = tar.FormatPAX
	root.header.PAXRecords = map[string]string{"comment": "hidden"}
	assertDockerArchiveRejected(t, candidateArchive(t, root), dockerArchiveLimits())
	root = dockerArchiveRoot()
	root.header.Format = tar.FormatGNU
	assertDockerArchiveRejected(t, candidateArchive(t, root), dockerArchiveLimits())
}

func testDockerArchivePaths(t *testing.T) {
	t.Helper()
	for _, name := range []string{"./../escape", "./.git/config", "./dir/.GiT/config", ".//absolute", "./dir//file", "./dir/./file", "./dir\\file", "./CON", "./trailing ", "./name.", "./name:stream"} {
		assertDockerArchiveRejected(t, candidateArchive(t, dockerArchiveRoot(), dockerArchiveFile(name, "x")), dockerArchiveLimits())
	}
	for _, names := range [][]string{{"./same", "./same"}, {"./Case", "./case"}, {"./dir/file", "./Dir/other"}, {"./file", "./file/child"}} {
		assertDockerArchiveRejected(t, candidateArchive(t, dockerArchiveRoot(), dockerArchiveFile(names[0], "a"), dockerArchiveFile(names[1], "b")), dockerArchiveLimits())
	}
}

func testDockerArchiveBounds(t *testing.T) {
	t.Helper()
	archive := candidateArchive(t, dockerArchiveRoot(), dockerArchiveFile("./dir/file", "123456"))
	for _, mutate := range []func(*sourceLimits){
		func(l *sourceLimits) { l.MaxArchiveBytes = int64(len(archive)) - 1 }, func(l *sourceLimits) { l.MaxBytes = 5 },
		func(l *sourceLimits) { l.MaxFileBytes = 5 }, func(l *sourceLimits) { l.MaxFiles = 1 }, func(l *sourceLimits) { l.MaxPathBytes = 7 },
	} {
		limits := dockerArchiveLimits()
		mutate(&limits)
		assertDockerArchiveRejected(t, archive, limits)
	}
	limits := dockerArchiveLimits()
	limits.MaxArchiveBytes = int64(len(archive))
	limits.MaxPathBytes = len("dir/file")
	limits.MaxFiles = 2
	if got, err := decodeDockerSourceArchive(bytes.NewReader(archive), limits); err != nil || len(got) != 1 {
		t.Fatalf("normalized exact bounds rejected: %v", err)
	}
	reader := &countingArchiveReader{Reader: io.MultiReader(bytes.NewReader(archive), io.LimitReader(zeroArchiveReader{}, 1<<30))}
	if _, err := decodeDockerSourceArchive(reader, limits); err == nil || reader.read > limits.MaxArchiveBytes+1 {
		t.Fatalf("unbounded stream read=%d err=%v", reader.read, err)
	}
	for _, bad := range [][]byte{archive[:512], archive[:len(archive)-512], append(append([]byte(nil), archive...), archive...), append(append([]byte(nil), archive...), 'x')} {
		assertDockerArchiveRejected(t, bad, dockerArchiveLimits())
	}
	if _, err := decodeDockerSourceArchive(failingArchiveReader{}, limits); err == nil {
		t.Fatal("reader error ignored")
	}
}

func testDockerArchiveEncode(t *testing.T) {
	t.Helper()
	entries := []sourceEntry{{Path: "dir/empty", Mode: 0o100644}, {Path: "run", Mode: 0o100755, Data: []byte("raw\x00\n")}}
	archive, err := encodeDockerSourceArchive(entries, dockerArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSourceArchive(bytes.NewReader(archive), dockerArchiveLimits())
	if err != nil || len(decoded) != 2 {
		t.Fatalf("trusted seed rejected: %v", err)
	}
	stream := tar.NewReader(bytes.NewReader(archive))
	var names []string
	for {
		h, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Uid != 1000 || h.Gid != 1000 || h.Format != tar.FormatUSTAR || strings.HasPrefix(h.Name, "./") {
			t.Fatalf("untrusted seed metadata: %+v", h)
		}
	}
	if !reflect.DeepEqual(names, []string{"dir/", "dir/empty", "run"}) {
		t.Fatalf("implicit directory missing from seed: %v", names)
	}
	if decoded[1].Mode != 0o100755 || !bytes.Equal(decoded[1].Data, entries[1].Data) {
		t.Fatal("seed changed executable bytes")
	}
	if again, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err != nil || !bytes.Equal(archive, again) {
		t.Fatal("trusted seed is not deterministic")
	}
	if again, err := encodeDockerSourceArchive([]sourceEntry{entries[1], entries[0]}, dockerArchiveLimits()); err != nil || !bytes.Equal(archive, again) {
		t.Fatal("trusted seed depends on caller entry order")
	}
	long := strings.Repeat("d", 80) + "/" + strings.Repeat("f", 84)
	if _, err := encodeDockerSourceArchive([]sourceEntry{{Path: long, Mode: 0o100644}}, dockerArchiveLimits()); err != nil {
		t.Fatalf("representable split USTAR path rejected: %v", err)
	}
}

func testDockerArchiveEncodeLimits(t *testing.T) {
	t.Helper()
	for _, entries := range [][]sourceEntry{
		{{Path: "../escape", Mode: 0o100644}},
		{{Path: "a", Mode: 0o120000}},
		{{Path: "Case", Mode: 0o100644}, {Path: "case", Mode: 0o100644}},
		{{Path: strings.Repeat("z", 101), Mode: 0o100644}},
		{{Path: strings.Repeat("a", 80) + "/" + strings.Repeat("b", 74) + "/" + strings.Repeat("c", 99), Mode: 0o100644}},
		{{Path: "large", Mode: 0o100644, Data: make([]byte, 2049)}},
	} {
		if data, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err == nil || data != nil {
			t.Fatal("unsupported seed emitted bytes")
		}
	}
	limits := dockerArchiveLimits()
	limits.MaxArchiveBytes = 512
	if data, err := encodeDockerSourceArchive([]sourceEntry{{Path: "file", Mode: 0o100644, Data: []byte("x")}}, limits); err == nil || data != nil {
		t.Fatal("seed exceeded complete archive bound")
	}
	for _, mutate := range []func(*sourceLimits){
		func(l *sourceLimits) { l.MaxArchiveBytes = 0 },
		func(l *sourceLimits) { l.MaxArchiveBytes = 511 },
		func(l *sourceLimits) { l.MaxArchiveBytes = 2047 },
		func(l *sourceLimits) { l.MaxBytes = 1 },
		func(l *sourceLimits) { l.MaxFiles = 1 },
		func(l *sourceLimits) { l.MaxPathBytes = 3 },
	} {
		limits = dockerArchiveLimits()
		mutate(&limits)
		if data, err := encodeDockerSourceArchive([]sourceEntry{{Path: "dir/file", Mode: 0o100644, Data: []byte("xx")}}, limits); err == nil || data != nil {
			t.Fatal("seed exceeded an admitted source bound")
		}
	}
	limits = dockerArchiveLimits()
	limits.MaxArchiveBytes = 1024
	if data, err := encodeDockerSourceArchive(nil, limits); err != nil || len(data) != 1024 {
		t.Fatalf("empty seed=%d bytes, %v", len(data), err)
	}
	limits.MaxArchiveBytes--
	if data, err := encodeDockerSourceArchive(nil, limits); err == nil || data != nil {
		t.Fatal("empty seed exceeded end-block bound")
	}
}

func assertDockerArchiveRejected(t *testing.T, data []byte, limits sourceLimits) {
	t.Helper()
	if got, err := decodeDockerSourceArchive(bytes.NewReader(data), limits); err == nil || got != nil {
		t.Fatalf("unsafe Docker archive accepted: %+v, %v", got, err)
	}
}

func testDockerArchiveLinks(t *testing.T) {
	t.Helper()
	entries := []sourceEntry{
		{Path: ".claude/hooks/context_block_stop.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_block_stop.py")},
		{Path: ".claude/hooks/context_pct_writer.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_pct_writer.py")},
		{Path: "assets/hooks/context_block_stop.py", Mode: 0o100755, Data: []byte("#!/usr/bin/python\n")},
		{Path: "assets/hooks/context_pct_writer.py", Mode: 0o100644, Data: []byte("raw\x00\xff\n")},
		{Path: "alias", Mode: 0o120000, Data: []byte("assets/../assets/hooks/context_pct_writer.py")},
	}
	archive, err := encodeDockerSourceArchive(entries, dockerArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	stream := tar.NewReader(bytes.NewReader(archive))
	linkSeen, links := false, 0
	for {
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Uid != 1000 || header.Gid != 1000 || header.Format != tar.FormatUSTAR {
			t.Fatalf("seed ownership or format changed: %+v", header)
		}
		if header.Typeflag != tar.TypeSymlink {
			if linkSeen {
				t.Fatalf("non-link %q emitted after a link", header.Name)
			}
			continue
		}
		linkSeen, links = true, links+1
		body, err := io.ReadAll(stream)
		if err != nil || header.Size != 0 || header.Mode != 0o777 || len(body) != 0 {
			t.Fatalf("link carries mode/body: %+v, %q, %v", header, body, err)
		}
	}
	if links != 3 {
		t.Fatalf("seed contains %d links", links)
	}
	for _, data := range [][]byte{archive, dockerArchiveExportFixture(t, archive)} {
		decode := decodeSourceArchive
		if !bytes.Equal(data, archive) {
			decode = decodeDockerSourceArchive
		}
		got, err := decode(bytes.NewReader(data), dockerArchiveLimits())
		if err != nil || len(got) != len(entries) {
			t.Fatalf("link round trip: %+v, %v", got, err)
		}
		for _, expected := range entries {
			found := false
			for _, actual := range got {
				found = found || reflect.DeepEqual(actual, expected)
			}
			if !found {
				t.Fatalf("raw source entry changed: %+v", expected)
			}
		}
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if again, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err != nil || !bytes.Equal(again, archive) {
		t.Fatalf("link seed depends on input order: %v", err)
	}
}

// This is a synthetic Docker root/prefix envelope, not native symlink evidence.
// The earlier native capture contains regular files only; capture is separate.
func dockerArchiveExportFixture(t *testing.T, seed []byte) []byte {
	t.Helper()
	entries := []archiveFixtureEntry{dockerArchiveRoot()}
	stream := tar.NewReader(bytes.NewReader(seed))
	for {
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(stream)
		if err != nil {
			t.Fatal(err)
		}
		header.Name = "./" + header.Name
		entries = append(entries, archiveFixtureEntry{header: *header, data: string(body)})
	}
	return candidateArchive(t, entries...)
}

func testDockerArchiveLinkQuotas(t *testing.T) {
	t.Helper()
	entries := []sourceEntry{{Path: "d/link", Mode: 0o120000, Data: []byte("../file")}, {Path: "file", Mode: 0o100644, Data: []byte("x")}}
	seed, err := encodeDockerSourceArchive(entries, dockerArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	export := dockerArchiveExportFixture(t, seed)
	limits := sourceLimits{MaxArchiveBytes: int64(len(seed)), MaxBytes: 8, MaxFileBytes: 7, MaxFiles: 3, MaxPathBytes: 6}
	if data, err := encodeDockerSourceArchive(entries, limits); err != nil || !bytes.Equal(data, seed) {
		t.Fatalf("exact link seed quotas rejected: %v", err)
	}
	decodeLimits := limits
	decodeLimits.MaxArchiveBytes = int64(len(export))
	if got, err := decodeDockerSourceArchive(bytes.NewReader(export), decodeLimits); err != nil || len(got) != 2 {
		t.Fatalf("exact Docker link quotas rejected: %+v, %v", got, err)
	}
	for _, lower := range []func(*sourceLimits){
		func(l *sourceLimits) { l.MaxArchiveBytes-- },
		func(l *sourceLimits) { l.MaxBytes-- },
		func(l *sourceLimits) { l.MaxFileBytes-- },
		func(l *sourceLimits) { l.MaxFiles-- },
		func(l *sourceLimits) { l.MaxPathBytes-- },
	} {
		bounded := limits
		lower(&bounded)
		if data, err := encodeDockerSourceArchive(entries, bounded); err == nil || data != nil {
			t.Fatalf("link seed exceeded quota: %+v", bounded)
		}
		bounded = decodeLimits
		lower(&bounded)
		assertDockerArchiveRejected(t, export, bounded)
	}
}

func testDockerArchiveLinkVectors(t *testing.T) {
	t.Helper()
	base := []sourceEntry{{Path: "file", Mode: 0o100644, Data: []byte("x")}, {Path: "dir/child", Mode: 0o100644}}
	for _, target := range []string{"", "../file", "/file", "./file", "missing/../file", "file/../file", "DIR/../file", "dir", "FILE", "other", ".git/../file", "file\x00", strings.Repeat("a", 101)} {
		entries := append([]sourceEntry{{Path: "link", Mode: 0o120000, Data: []byte(target)}}, base...)
		if data, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err == nil || data != nil {
			t.Fatalf("unsafe raw target emitted seed: %q", target)
		}
	}
	for _, extra := range [][]sourceEntry{
		{{Path: "file", Mode: 0o120000, Data: []byte("dir/child")}},
		{{Path: "FILE", Mode: 0o120000, Data: []byte("file")}},
		{{Path: "dir", Mode: 0o120000, Data: []byte("file")}},
		{{Path: "link", Mode: 0o120000, Data: []byte("file")}, {Path: "link/child", Mode: 0o100644}},
		{{Path: "link", Mode: 0o120000, Data: []byte("other")}, {Path: "other", Mode: 0o120000, Data: []byte("file")}},
		{{Path: "link", Mode: 0o120777, Data: []byte("file")}},
	} {
		entries := append(extra, base...)
		if data, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err == nil || data != nil {
			t.Fatalf("conflicting link vector emitted seed: %+v", extra)
		}
	}
}

func testDockerArchiveLinkMetadata(t *testing.T) {
	t.Helper()
	link := sourceLinkFixture("./link", "file")
	file := dockerArchiveFile("./file", "x")
	archive := candidateArchive(t, dockerArchiveRoot(), link, file)
	got, err := decodeDockerSourceArchive(bytes.NewReader(archive), dockerArchiveLimits())
	if err != nil || len(got) != 2 || got[0].Mode != 0o120000 || string(got[0].Data) != "file" {
		t.Fatalf("link preceding target rejected or dereferenced: %+v, %v", got, err)
	}
	copy(archive[512+124:512+136], "00000000001\x00")
	fixDockerArchiveChecksum(archive[512:1024])
	assertDockerArchiveRejected(t, archive, dockerArchiveLimits())
	file.header.Linkname = "link"
	assertDockerArchiveRejected(t, candidateArchive(t, dockerArchiveRoot(), file), dockerArchiveLimits())
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		link.header.Linkname, link.header.Format = strings.Repeat("t", 101), format
		assertDockerArchiveRejected(t, candidateArchive(t, dockerArchiveRoot(), link), dockerArchiveLimits())
	}
}

func testDockerArchiveLinkUSTAR(t *testing.T) {
	t.Helper()
	target := strings.Repeat("t", 100)
	entries := []sourceEntry{{Path: "link", Mode: 0o120000, Data: []byte(target)}, {Path: target, Mode: 0o100644, Data: []byte("x")}}
	seed, err := encodeDockerSourceArchive(entries, dockerArchiveLimits())
	if err != nil {
		t.Fatalf("100 byte USTAR target rejected: %v", err)
	}
	if got, err := decodeDockerSourceArchive(bytes.NewReader(dockerArchiveExportFixture(t, seed)), dockerArchiveLimits()); err != nil || len(got) != 2 {
		t.Fatalf("100 byte target cannot round trip: %+v, %v", got, err)
	}
	prefix := strings.Repeat("a", 80)
	entries = []sourceEntry{{Path: prefix + "/file", Mode: 0o100644}, {Path: prefix + "/" + strings.Repeat("b", 74) + "/" + strings.Repeat("c", 99), Mode: 0o120000, Data: []byte("../file")}}
	if data, err := encodeDockerSourceArchive(entries, dockerArchiveLimits()); err == nil || data != nil {
		t.Fatal("link path requiring PAX after Docker prefix emitted a seed")
	}
}
