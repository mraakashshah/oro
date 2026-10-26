package factory //nolint:testpackage // Exercise raw Git objects and the private managed ownership seam.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCandidateExportExactTree(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) { testSourceGitExport(t, format) })
	}
	t.Run("unsupported trees fail closed", testSourceGitUnsupported)
	t.Run("bounded and authenticated raw output", testSourceGitOutput)
}

func TestCandidateCommitAvoidsFilters(t *testing.T) {
	t.Run("exact tree deterministic commit and CAS", testSourceGitCommit)
	t.Run("invalid vector has no host effects", testSourceGitInvalidEntries)
	t.Run("external advance survives CAS", testSourceGitCAS)
	t.Run("target identity and checked-out guard", testSourceGitTarget)
}

func TestCandidateInternalLinkExactTree(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) { testSourceGitLinks(t, format) })
	}
	t.Run("unsafe vectors precede host effects", testSourceGitUnsafeLinks)
	t.Run("unsafe exported trees fail closed", testSourceGitUnsupported)
	t.Run("link transport corruption fails closed", testSourceGitLinkOutput)
}

func TestCandidateCommitExistingBlobs(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) { testSourceGitPresenceCounts(t, format) })
	}
	t.Run("malformed presence precedes writes", testSourceGitPresenceMalformed)
	t.Run("pure presence protocol", testSourceGitPresenceProtocol)
	t.Run("query and write failure boundaries", testSourceGitPresenceFailure)
	t.Run("budget is never refunded", testSourceGitPresenceBudget)
	t.Run("replacement refs do not redirect presence", testSourceGitPresenceReplacement)
	t.Run("presence is not a raw content proof", testSourceGitPresenceCorruption)
}

func testSourceGitPresenceCounts(t *testing.T, format string) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, func(repo string) {
		if format == "sha256" {
			if err := os.Mkdir(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			gitTestRun(t, repo, "init", "--object-format=sha256", "-b", "main")
		}
		for index := range 20 {
			gitTestWrite(t, filepath.Join(repo, fmt.Sprintf("file-%02d", index)), fmt.Sprintf("original-%d\n", index), 0o600)
		}
		gitTestRun(t, repo, "add", ".")
		gitTestRun(t, repo, "commit", "-m", "many original blobs")
		gitTestRun(t, repo, "branch", "-f", "delivery")
	}, func(spec *ManagedSpec) {
		if format == "sha256" {
			spec.Repository = filepath.Join(spec.Repository, "sha256")
		}
	})
	entries, err := g.export(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceGitCountedCommit(t, g, runtime, base, entries, 1); got != base {
		t.Fatal("no-op did not retain exact base")
	}
	index := slices.IndexFunc(entries, func(entry sourceEntry) bool { return entry.Path == "file-00" })
	entries[index].Data = []byte("one new identity\n")
	entries = append(entries, sourceEntry{Path: "duplicate", Mode: 0o100644, Data: entries[index].Data})
	candidate := sourceGitCountedCommit(t, g, runtime, base, entries, 6)
	var firstQuery []byte
	for _, call := range runtime.calls {
		if slices.Contains(call.Args, "--batch-check") {
			firstQuery = call.Stdin
		}
	}
	if again := sourceGitCountedCommit(t, g, runtime, base, entries, 5); again != candidate {
		t.Fatal("already-present replay changed candidate")
	}
	for _, call := range runtime.calls[len(runtime.calls)-5:] {
		if slices.Contains(call.Args, "--batch-check") && !bytes.Equal(call.Stdin, firstQuery) {
			t.Fatal("identical candidate produced nondeterministic presence input")
		}
	}
	for missing := range 3 {
		entries = append(entries, sourceEntry{Path: fmt.Sprintf("new-%d", missing), Mode: 0o100644, Data: []byte(fmt.Sprintf("new blob %d", missing))})
	}
	candidate = sourceGitCountedCommit(t, g, runtime, candidate, entries, 8)
	got, err := g.export(t.Context(), candidate)
	slices.SortFunc(entries, func(a, b sourceEntry) int { return strings.Compare(a.Path, b.Path) })
	if err != nil || !reflect.DeepEqual(got, entries) {
		t.Fatalf("presence reuse changed exact candidate tree: %+v, %v", got, err)
	}
}

func sourceGitCountedCommit(t *testing.T, g *managedGitRepository, runtime *sourceGitRuntime, base string, entries []sourceEntry, want int) string {
	t.Helper()
	before, spent := len(runtime.calls), sourceGitHostSpend(t, runtime)
	candidate, err := g.commit(t.Context(), base, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.calls)-before != want || sourceGitHostSpend(t, runtime)-spent != want {
		t.Fatalf("commit spent %d calls, want %d", len(runtime.calls)-before, want)
	}
	queries := 0
	for _, call := range runtime.calls[before:] {
		if !call.Deadline.Equal(runtime.calls[before].Deadline) {
			t.Fatal("presence or writes refreshed the operation deadline")
		}
		if slices.Contains(call.Args, "--batch-check") {
			queries++
			tree, err := buildSourceTree(entries, g.source, len(base))
			if err != nil {
				t.Fatal(err)
			}
			var expected []string
			for _, oid := range tree.blobs {
				if !slices.Contains(expected, oid) {
					expected = append(expected, oid)
				}
			}
			if string(call.Stdin) != strings.Join(expected, "\n")+"\n" || !slices.Contains(call.Args, "--no-replace-objects") {
				t.Fatalf("presence queried identities other than local candidate bytes: %q", call.Stdin)
			}
		}
	}
	if (want == 1 && queries != 0) || (want != 1 && queries != 1) {
		t.Fatalf("commit issued %d presence queries", queries)
	}
	return candidate
}

func sourceGitHostSpend(t *testing.T, runtime *sourceGitRuntime) int {
	t.Helper()
	var spent int
	if err := runtime.j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='host'").Scan(&spent); err != nil {
		t.Fatal(err)
	}
	return spent
}

func testSourceGitPresenceMalformed(t *testing.T) {
	t.Helper()
	for _, failure := range []string{"oid", "width", "type", "size", "leading zero", "order", "duplicate", "extra", "truncated", "missing form", "long row"} {
		t.Run(failure, func(t *testing.T) {
			g, runtime, _, base := sourceGitFixture(t, nil, nil)
			runtime.alter = func(call HostInvocation, result HostResult) HostResult {
				if slices.Contains(call.Args, "--batch-check") {
					result.Stdout = sourceGitBadPresence(result.Stdout, failure)
				}
				return result
			}
			entries := []sourceEntry{{Path: "source.txt", Mode: 0o100644, Data: []byte("original\n")}, {Path: "z-new", Mode: 0o100644, Data: []byte("missing")}}
			if candidate, err := g.commit(t.Context(), base, entries); err == nil || candidate != "" {
				t.Fatalf("malformed presence permitted candidate write: %q, %v", candidate, err)
			}
			if len(runtime.calls) != 2 || sourceGitHostSpend(t, runtime) != 2 {
				t.Fatal("presence error wrote objects or refunded its two consumed calls")
			}
		})
	}
}

func sourceGitBadPresence(data []byte, failure string) []byte {
	rows := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	fields := strings.Split(rows[0], " ")
	switch failure {
	case "oid":
		fields[0] = strings.Repeat("0", len(fields[0]))
	case "width":
		fields[0] = fields[0][1:]
	case "type":
		fields[1] = "tree"
	case "size":
		fields[2] = "1"
	case "leading zero":
		fields[2] = "0" + fields[2]
	case "order":
		return []byte(rows[1] + "\n" + rows[0] + "\n")
	case "duplicate":
		return []byte(rows[0] + "\n" + rows[0] + "\n")
	case "extra":
		return append(data, []byte(rows[0]+"\n")...)
	case "truncated":
		return data[:len(data)-1]
	case "missing form":
		return []byte(rows[0] + "\n" + rows[1] + " extra\n")
	case "long row":
		return []byte(strings.Repeat("x", 129) + "\n")
	}
	rows[0] = strings.Join(fields, " ")
	return []byte(strings.Join(rows, "\n") + "\n")
}

func testSourceGitPresenceBudget(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, func(spec *ManagedSpec) { spec.Runtime.MaxHostInvocations = 2 })
	entries := []sourceEntry{{Path: "added", Mode: 0o100644, Data: []byte("missing")}}
	if _, err := g.commit(t.Context(), base, entries); err == nil || len(runtime.calls) != 2 || !slices.Contains(runtime.calls[1].Args, "--batch-check") {
		t.Fatalf("presence budget did not precede writes: %v", err)
	}
	if _, err := g.commit(t.Context(), base, entries); err == nil || len(runtime.calls) != 2 || sourceGitHostSpend(t, runtime) != 2 {
		t.Fatal("failed commit refunded its exhausted budget")
	}
}

func testSourceGitPresenceProtocol(t *testing.T) {
	t.Helper()
	for _, width := range []int{40, 64} {
		oid, other := strings.Repeat("a", width), strings.Repeat("b", width)
		data := []byte(oid + " blob 0\n" + other + " missing\n")
		got, err := decodeSourceBlobPresence(data, []string{oid, other}, []int64{0, 9})
		if err != nil || !reflect.DeepEqual(got, []bool{true, false}) {
			t.Fatalf("valid %d-wide presence rejected: %v, %v", width, got, err)
		}
	}
	if got, err := decodeSourceBlobPresence(nil, nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty presence query rejected: %v, %v", got, err)
	}
	oid, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for name, input := range map[string]struct {
		oids  []string
		sizes []int64
	}{
		"length mismatch": {oids: []string{oid}},
		"negative size":   {oids: []string{oid}, sizes: []int64{-1}},
		"uppercase":       {oids: []string{strings.ToUpper(oid)}, sizes: []int64{9}},
		"invalid hex":     {oids: []string{strings.Repeat("g", 40)}, sizes: []int64{9}},
		"short identity":  {oids: []string{oid[1:]}, sizes: []int64{9}},
		"mixed widths":    {oids: []string{oid, strings.Repeat("b", 64)}, sizes: []int64{9, 9}},
		"duplicate input": {oids: []string{oid, oid}, sizes: []int64{9, 9}},
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(strings.Join(input.oids, " missing\n") + " missing\n")
			if got, err := decodeSourceBlobPresence(data, input.oids, input.sizes); err == nil || got != nil {
				t.Fatalf("invalid local query accepted: %v, %v", got, err)
			}
		})
	}
	for _, row := range []string{
		oid + " blob +9\n", oid + " blob 09\n", oid + " blob -9\n", oid + " blob 9223372036854775808\n",
		oid + "  blob 9\n", oid + " blob 9 \n", " " + oid + " blob 9\n", oid + " missing\r\n",
		oid + " missing 0\n", oid + " ambiguous\n", other + " missing\n", oid + " blob 9\n\n",
	} {
		if got, err := decodeSourceBlobPresence([]byte(row), []string{oid}, []int64{9}); err == nil || got != nil {
			t.Fatalf("noncanonical presence row accepted: %q, %v, %v", row, got, err)
		}
	}
}

func testSourceGitPresenceFailure(t *testing.T) {
	t.Helper()
	for _, failure := range []string{"cancellation", "overflow", "nonzero", "runtime", "write oid"} {
		t.Run(failure, func(t *testing.T) {
			g, runtime, _, base := sourceGitFixture(t, nil, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runtime.before = func(call HostInvocation) {
				if failure == "runtime" && slices.Contains(call.Args, "--batch-check") {
					runtime.failure = errors.New("presence ownership uncertain")
				}
			}
			runtime.alter = func(call HostInvocation, result HostResult) HostResult {
				if slices.Contains(call.Args, "--batch-check") {
					switch failure {
					case "cancellation":
						cancel()
					case "overflow":
						result.Stdout = make([]byte, call.OutputLimit+1)
					case "nonzero":
						result.ExitCode = 23
					}
				}
				if failure == "write oid" && slices.Contains(call.Args, "hash-object") {
					result.Stdout = []byte(strings.Repeat("0", len(base)) + "\n")
				}
				return result
			}
			entries := []sourceEntry{{Path: "added", Mode: 0o100644, Data: []byte("new bytes")}}
			if candidate, err := g.commit(ctx, base, entries); err == nil || candidate != "" {
				t.Fatalf("failed presence/write returned candidate: %q, %v", candidate, err)
			}
			wantCalls := 2
			if failure == "write oid" {
				wantCalls++
			}
			if len(runtime.calls) != wantCalls || sourceGitHostSpend(t, runtime) != wantCalls {
				t.Fatal("failed presence/write continued or refunded its spent calls")
			}
		})
	}
}

func testSourceGitPresenceReplacement(t *testing.T) {
	t.Helper()
	g, runtime, manifest, base := sourceGitFixture(t, nil, nil)
	data := []byte("original\n")
	oid := sourceObjectID("blob", data, len(base))
	other := sourceGitInput(t, manifest.Spec.Repository, []byte("replacement of a different size"), "hash-object", "-w", "--stdin")
	gitTestRun(t, manifest.Spec.Repository, "replace", oid, other)
	entries := []sourceEntry{{Path: "source.txt", Mode: 0o100644, Data: data}, {Path: "copy", Mode: 0o100644, Data: data}}
	candidate := sourceGitCountedCommit(t, g, runtime, base, entries, 5)
	got, err := g.export(t.Context(), candidate)
	slices.Reverse(entries)
	if err != nil || !reflect.DeepEqual(got, entries) {
		t.Fatalf("replacement ref changed reused bytes: %+v, %v", got, err)
	}
}

func testSourceGitPresenceCorruption(t *testing.T) {
	t.Helper()
	g, runtime, manifest, base := sourceGitFixture(t, nil, nil)
	data := []byte("original\n")
	oid := sourceObjectID("blob", data, len(base))
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte("blob 9\x00tampered\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manifest.Spec.Repository, ".git", "objects", oid[:2], oid[2:])
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []sourceEntry{{Path: "source.txt", Mode: 0o100644, Data: data}, {Path: "copy", Mode: 0o100644, Data: data}}
	candidate := sourceGitCountedCommit(t, g, runtime, base, entries, 5)
	if got, err := g.export(t.Context(), candidate); err == nil || got != nil || !strings.Contains(err.Error(), "bytes do not match") {
		t.Fatalf("presence metadata substituted for raw content proof: %+v, %v", got, err)
	}
	if actual := gitTestRun(t, manifest.Spec.Repository, "rev-parse", "delivery"); actual != base {
		t.Fatal("unverified corrupted content advanced the target")
	}
}

func sourceGitLinkFixture(t *testing.T, format string) (*managedGitRepository, *sourceGitRuntime, managedManifest, string, []sourceEntry, string) {
	t.Helper()
	want := []sourceEntry{
		{Path: ".gitattributes", Mode: 0o100644, Data: []byte(".claude/hooks/* export-ignore\nassets/hooks/context_block_stop.py export-subst\n* filter=hostile\n")},
		{Path: ".claude/hooks/context_block_stop.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_block_stop.py")},
		{Path: ".claude/hooks/context_pct_writer.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_pct_writer.py")},
		{Path: "assets/hooks/context_block_stop.py", Mode: 0o100644, Data: []byte("$Format:%H$\n")},
		{Path: "assets/hooks/context_pct_writer.py", Mode: 0o100755, Data: []byte("executable\n")},
	}
	marker := filepath.Join(t.TempDir(), "executed")
	g, runtime, manifest, base := sourceGitFixture(t, func(repo string) {
		if format == "sha256" {
			if err := os.Mkdir(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			gitTestRun(t, repo, "init", "--object-format=sha256", "-b", "main")
		}
		for _, entry := range want {
			path := filepath.Join(repo, entry.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if entry.Mode == 0o120000 {
				if err := os.Symlink(string(entry.Data), path); err != nil {
					t.Fatal(err)
				}
				continue
			}
			gitTestWrite(t, path, string(entry.Data), os.FileMode(entry.Mode&0o777))
		}
		gitTestRun(t, repo, "add", ".")
		gitTestRun(t, repo, "commit", "-m", "internal source links")
		gitTestRun(t, repo, "branch", "-f", "delivery")
		hooks := t.TempDir()
		program := "#!/bin/sh\nprintf executed > " + gitTestShellQuote(marker) + "\ncat\n"
		for _, name := range []string{"filter", "pre-commit", "reference-transaction"} {
			gitTestWrite(t, filepath.Join(hooks, name), program, 0o700)
		}
		gitTestRun(t, repo, "config", "filter.hostile.clean", filepath.Join(hooks, "filter"))
		gitTestRun(t, repo, "config", "filter.hostile.smudge", filepath.Join(hooks, "filter"))
		gitTestRun(t, repo, "config", "core.hooksPath", hooks)
	}, func(spec *ManagedSpec) {
		if format == "sha256" {
			spec.Repository = filepath.Join(spec.Repository, "sha256")
		}
	})
	return g, runtime, manifest, base, want, marker
}

func testSourceGitLinks(t *testing.T, format string) {
	t.Helper()
	g, runtime, manifest, base, want, marker := sourceGitLinkFixture(t, format)
	entries, err := g.export(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range want {
		index := slices.IndexFunc(entries, func(actual sourceEntry) bool { return actual.Path == entry.Path })
		if index < 0 || !reflect.DeepEqual(entries[index], entry) {
			t.Fatalf("raw link/tree entry %q changed", entry.Path)
		}
	}
	repo := manifest.Spec.Repository
	tree, err := buildSourceTree(entries, g.source, len(base))
	if err != nil || tree.oid != gitTestRun(t, repo, "rev-parse", base+"^{tree}") {
		t.Fatalf("export lost exact %s tree identity: %v", format, err)
	}
	if noOp, err := g.commit(t.Context(), base, entries); err != nil || noOp != base {
		t.Fatalf("link no-op changed base: %q, %v", noOp, err)
	}
	index := slices.IndexFunc(entries, func(entry sourceEntry) bool { return entry.Path == want[1].Path })
	entries[index].Data = append([]byte(nil), want[2].Data...)
	candidate, err := g.commit(t.Context(), base, entries)
	if err != nil || candidate == base {
		t.Fatalf("safe changed target did not commit: %q, %v", candidate, err)
	}
	got, err := g.export(t.Context(), candidate)
	if err != nil || !reflect.DeepEqual(got, entries) {
		t.Fatalf("safe target changed on round-trip: %+v, %v", got, err)
	}
	changedTree, err := buildSourceTree(entries, g.source, len(base))
	if err != nil || changedTree.oid != gitTestRun(t, repo, "rev-parse", candidate+"^{tree}") {
		t.Fatalf("changed target lost exact tree identity: %v", err)
	}
	if raw := gitTestRun(t, repo, "cat-file", "blob", candidate+":"+want[1].Path); raw != string(want[2].Data) {
		t.Fatalf("Git stored dereferenced or normalized link: %q", raw)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("link transport executed an attribute filter or hook: %v", err)
	}
	for _, call := range runtime.calls {
		if slices.Contains(call.Args, "archive") || !slices.Contains(call.Args, "--no-replace-objects") {
			t.Fatalf("link transport bypassed raw Git protections: %v", call.Args)
		}
	}
}

func testSourceGitUnsafeLinks(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, nil)
	file := sourceEntry{Path: "file", Mode: 0o100644, Data: []byte("safe")}
	for _, entries := range [][]sourceEntry{
		{file, {Path: "link", Mode: 0o120000, Data: []byte("../outside")}},
		{file, {Path: "link", Mode: 0o120000, Data: []byte("missing/../file")}},
		{file, {Path: "link", Mode: 0o120000, Data: []byte("file/../file")}},
		{file, {Path: "link", Mode: 0o120000, Data: []byte("other")}, {Path: "other", Mode: 0o120000, Data: []byte("file")}},
		{file, {Path: "link", Mode: 0o120000, Data: []byte("file")}, {Path: "link/child", Mode: 0o100644}},
		{file, {Path: "module", Mode: 0o160000}},
	} {
		if _, err := g.commit(t.Context(), base, entries); err == nil {
			t.Fatalf("unsafe linked vector committed: %+v", entries)
		}
	}
	if len(runtime.calls) != 0 {
		t.Fatal("unsafe candidate reached HostRuntime before whole-vector validation")
	}
}

func testSourceGitLinkOutput(t *testing.T) {
	t.Helper()
	g, runtime, _, base, _, _ := sourceGitLinkFixture(t, "sha1")
	for _, failure := range []string{"mode", "size", "unterminated", "blob hash"} {
		t.Run(failure, func(t *testing.T) {
			runtime.alter = func(call HostInvocation, result HostResult) HostResult {
				if failure == "blob hash" {
					if index := bytes.Index(result.Stdout, []byte("../../assets/hooks/context_block_stop.py")); slices.Contains(call.Args, "cat-file") && index >= 0 {
						result.Stdout[index] = 'x'
					}
					return result
				}
				if slices.Contains(call.Args, "ls-tree") {
					switch failure {
					case "mode":
						result.Stdout = bytes.Replace(result.Stdout, []byte("120000 blob "), []byte("0120000 blob "), 1)
					case "size":
						result.Stdout = bytes.Replace(result.Stdout, []byte("40\t.claude/hooks/"), []byte("39\t.claude/hooks/"), 1)
					case "unterminated":
						result.Stdout = result.Stdout[:len(result.Stdout)-1]
					}
				}
				return result
			}
			if got, err := g.export(t.Context(), base); err == nil || got != nil {
				t.Fatalf("corrupted link transport accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestManagedGitAuthority(t *testing.T) {
	t.Run("budget precedes effects and never refills", testSourceGitBudget)
	t.Run("one immutable operation deadline", testSourceGitDeadline)
	t.Run("stale generation and identity rejected", testSourceGitIdentity)
	t.Run("uncertain runtime stops this repository", testSourceGitUncertain)
	t.Run("scope insert failure cannot refund budget", testSourceGitAllocationFailure)
	t.Run("request bounds precede backend effects", testSourceGitRequestBounds)
}

func TestManagedGitEmptyCommand(t *testing.T) {
	for _, method := range []string{"invoke", "run"} {
		for _, vector := range []string{"nil", "empty"} {
			for _, outcome := range []string{"runtime failure", "nonzero"} {
				t.Run(method+"/"+vector+"/"+outcome, func(t *testing.T) {
					g, runtime, _, base := sourceGitFixture(t, nil, func(spec *ManagedSpec) { spec.Runtime.MaxHostInvocations = 1 })
					ctx, cancel := g.operation(t.Context())
					defer cancel()
					deadline, _ := ctx.Deadline()
					if outcome == "runtime failure" {
						runtime.failure = errors.New("injected runtime failure")
					} else {
						runtime.alter = func(_ HostInvocation, result HostResult) HostResult { result.ExitCode = 17; return result }
					}
					counts := func() [3]int {
						t.Helper()
						var got [3]int
						for i, query := range []string{"SELECT count(*) FROM managed_reservations WHERE kind='host'", "SELECT count(*) FROM managed_scopes", "SELECT count(*) FROM managed_events"} {
							if err := runtime.j.conn.QueryRowContext(t.Context(), query).Scan(&got[i]); err != nil {
								t.Fatal(err)
							}
						}
						return got
					}
					before := counts()
					var args []string
					if vector == "empty" {
						args = []string{}
					}
					func() {
						defer func() {
							if recovered := recover(); recovered != nil {
								t.Errorf("empty command panicked: %v", recovered)
							}
						}()
						var err error
						if method == "invoke" {
							_, err = g.invoke(ctx, nil, args...)
						} else {
							_, err = g.run(ctx, nil, args...)
						}
						if err == nil || err.Error() == "" {
							t.Error("empty command did not return an error")
						}
					}()
					if after := counts(); after != before || len(runtime.calls) != 0 {
						t.Errorf("empty command changed authority: counts %v -> %v, calls %d", before, after, len(runtime.calls))
					}
					runtime.failure, runtime.alter = nil, nil
					out, err := g.run(ctx, nil, "rev-parse", "refs/heads/delivery")
					if err != nil || strings.TrimSpace(string(out)) != base || len(runtime.calls) != 1 || !runtime.calls[0].Deadline.Equal(deadline) {
						t.Errorf("valid command lost original authority: output %q, error %v, calls %d", out, err, len(runtime.calls))
					}
				})
			}
		}
	}
}

// This adapter exercises real Git and real journal transactions. It deliberately
// does not qualify native process isolation; the launchd integration owns that.
type sourceGitRuntime struct {
	t       *testing.T
	j       *managedJournal
	calls   []HostInvocation
	before  func(HostInvocation)
	alter   func(HostInvocation, HostResult) HostResult
	failure error
}

func (r *sourceGitRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	r.t.Helper()
	observation, err := r.j.Observe(ctx, call.Scope)
	if err != nil || observation.Phase != "reserved" || !observation.Deadline.Equal(call.Deadline) {
		r.t.Fatalf("effect before exact scope reservation: %+v, %v", observation, err)
	}
	var generation uint64
	if err := r.j.conn.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", call.Scope.ID).Scan(&generation); err != nil || generation != call.Scope.Generation {
		r.t.Fatalf("effect before counted host reservation: %d, %v", generation, err)
	}
	a := r.j.admission
	digest := sha256.Sum256([]byte(a.Reservation.StateRoot + "\x00" + a.ObjectiveID))
	expected := "dev.getoro.factory." + hex.EncodeToString(digest[:8]) + ".host." + call.Scope.ID + "." + call.Scope.Nonce
	if call.Scope.Name != expected || call.Scope.Kind != "host" || call.Scope.Backend != "launchd" || call.Deadline.After(a.Deadline) {
		r.t.Fatalf("unbound host invocation: %+v", call)
	}
	r.calls = append(r.calls, call)
	if r.before != nil {
		r.before(call)
	}
	if r.failure != nil {
		return HostResult{}, r.failure
	}
	if err := r.j.RecordHello(ctx, call.Scope, 101, 101); err != nil {
		return HostResult{}, err
	}
	command := exec.CommandContext(ctx, call.Path, call.Args...)
	command.Dir, command.Env, command.Stdin = call.Dir, call.Env, bytes.NewReader(call.Stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	result := HostResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		err = nil
	}
	if err := errors.Join(err, r.j.MarkQuiescent(context.WithoutCancel(ctx), call.Scope)); err != nil {
		return HostResult{}, err
	}
	if r.alter != nil {
		result = r.alter(call, result)
	}
	return result, nil
}

func (r *sourceGitRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.j.MarkQuiescent(ctx, scope)
}

func sourceGitFixture(t *testing.T, setup func(string), change func(*ManagedSpec)) (*managedGitRepository, *sourceGitRuntime, managedManifest, string) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if runtime.GOOS == "darwin" {
		// /usr/bin/git is Apple's arm64e developer-tool launcher; its copied
		// image is not runnable. Freeze the actual selected Git executable.
		path, err := exec.Command("xcrun", "--find", "git").Output()
		if err != nil {
			t.Fatal(err)
		}
		spec.Runtime.GitExecutable = strings.TrimSpace(string(path))
	}
	spec.Runtime.SourceBytes = 4 << 20
	spec.Runtime.HostRequiredBytes = 1 << 40
	if change != nil {
		change(&spec)
	}
	reservation.Bytes = spec.Runtime.HostRequiredBytes
	if setup != nil {
		setup(spec.Repository)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := openJournalFixture(t, root, spec.ID)
	generation, _, err := j.beginGeneration(t.Context(), "source-git-supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &sourceGitRuntime{t: t, j: j}
	repository, err := newManagedGitRepository(manifest, generation, j, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return repository, runtime, manifest, gitTestRun(t, spec.Repository, "rev-parse", "delivery")
}

func testSourceGitExport(t *testing.T, format string) {
	t.Helper()
	var want []sourceEntry
	g, runtime, manifest, base := sourceGitFixture(t, func(repo string) {
		if format == "sha256" {
			// A separate repository avoids rewriting the SHA-1 fixture's objects.
			if err := os.Mkdir(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			gitTestRun(t, repo, "init", "--object-format=sha256", "-b", "main")
		}
		want = []sourceEntry{
			{Path: ".gitattributes", Mode: 0o100644, Data: []byte("failing.txt export-ignore\nsubst.txt export-subst\n")},
			{Path: "failing.txt", Mode: 0o100644, Data: []byte("FAIL\n")},
			{Path: "subst.txt", Mode: 0o100644, Data: []byte("$Format:%H$\n")},
			{Path: "dir/run", Mode: 0o100755, Data: []byte{0, 1, '\n', '\r', 255}},
			{Path: "dir.file", Mode: 0o100644, Data: []byte("Git tree sort sentinel")},
			{Path: "large", Mode: 0o100644, Data: bytes.Repeat([]byte{0, 'x'}, (1<<20)+1)},
		}
		for _, entry := range want {
			path := filepath.Join(repo, entry.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			gitTestWrite(t, path, string(entry.Data), os.FileMode(entry.Mode&0o777))
		}
		gitTestRun(t, repo, "add", ".")
		gitTestRun(t, repo, "commit", "-m", "raw source")
		gitTestRun(t, repo, "branch", "-f", "delivery")
	}, func(spec *ManagedSpec) {
		if format == "sha256" {
			spec.Repository = filepath.Join(spec.Repository, "sha256")
		}
	})
	got, err := g.export(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range want {
		index := slices.IndexFunc(got, func(actual sourceEntry) bool { return actual.Path == entry.Path })
		if index < 0 || !reflect.DeepEqual(got[index], entry) {
			t.Fatalf("raw entry %q changed", entry.Path)
		}
	}
	if len(runtime.calls) == 0 {
		t.Fatal("export bypassed managed host runtime")
	}
	for _, call := range runtime.calls {
		if call.Path != filepath.Join(manifest.Reservation.StateRoot, manifest.Spec.ID, "git") || slices.Contains(call.Args, "archive") {
			t.Fatalf("export used unfrozen or attribute-sensitive transport: %+v", call)
		}
	}
	// Replacement refs cannot alter the selected immutable commit.
	other := gitTestRun(t, manifest.Spec.Repository, "commit-tree", gitTestRun(t, manifest.Spec.Repository, "rev-parse", base+"^{tree}"), "-m", "replacement")
	gitTestRun(t, manifest.Spec.Repository, "replace", base, other)
	again, err := g.export(t.Context(), base)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("replacement changed raw export: %v", err)
	}
	got = append(got, sourceEntry{Path: "added", Mode: 0o100755, Data: []byte("candidate")})
	candidate, err := g.commit(t.Context(), base, got)
	if err != nil {
		t.Fatal(err)
	}
	if parent := gitTestRun(t, manifest.Spec.Repository, "--no-replace-objects", "show", "-s", "--format=%P", candidate); parent != base {
		t.Fatalf("%s candidate parent = %q", format, parent)
	}
	if _, err := g.export(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
}

func testSourceGitUnsupported(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"symlink", "gitlink", "empty-tree", "case-collision"} {
		t.Run(kind, func(t *testing.T) {
			g, _, manifest, base := sourceGitFixture(t, nil, nil)
			repo := manifest.Spec.Repository
			var tree string
			switch kind {
			case "symlink":
				if err := os.Symlink("outside", filepath.Join(repo, "link")); err != nil {
					t.Fatal(err)
				}
				gitTestRun(t, repo, "add", "link")
				tree = gitTestRun(t, repo, "write-tree")
			case "gitlink":
				tree = sourceGitInput(t, repo, []byte("160000 commit "+base+"\tmodule\x00"), "mktree", "-z")
			case "empty-tree":
				empty := sourceGitInput(t, repo, nil, "mktree")
				tree = sourceGitInput(t, repo, []byte("040000 tree "+empty+"\tempty\x00"), "mktree", "-z")
			case "case-collision":
				blob := sourceGitInput(t, repo, []byte("a"), "hash-object", "-w", "--stdin")
				tree = sourceGitInput(t, repo, []byte("100644 blob "+blob+"\tFILE\x00100644 blob "+blob+"\tfile\x00"), "mktree", "-z")
			}
			commit := gitTestRun(t, repo, "commit-tree", tree, "-p", base, "-m", "unsupported source")
			if _, err := g.export(t.Context(), commit); err == nil {
				t.Fatal("unsupported tree exported")
			}
		})
	}
}

func sourceGitInput(t *testing.T, repo string, input []byte, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir, command.Stdin, command.Env = repo, bytes.NewReader(input), gitEnvironment()
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Git fixture %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testSourceGitCommit(t *testing.T) {
	t.Helper()
	g, runtime, manifest, base := sourceGitFixture(t, nil, nil)
	repo := manifest.Spec.Repository
	entries, err := g.export(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := g.commit(t.Context(), base, entries); err != nil || got != base {
		t.Fatalf("no-change commit = %q, %v", got, err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	hook := filepath.Join(t.TempDir(), "program")
	gitTestWrite(t, hook, "#!/bin/sh\nprintf executed > "+gitTestShellQuote(marker)+"\ncat\n", 0o700)
	gitTestRun(t, repo, "config", "filter.hostile.clean", hook)
	gitTestRun(t, repo, "config", "filter.hostile.smudge", hook)
	gitTestRun(t, repo, "config", "core.hooksPath", filepath.Dir(hook))
	gitTestWrite(t, filepath.Join(filepath.Dir(hook), "pre-commit"), "#!/bin/sh\nprintf hook > "+gitTestShellQuote(marker)+"\n", 0o700)
	gitTestWrite(t, filepath.Join(filepath.Dir(hook), "reference-transaction"), "#!/bin/sh\nprintf ref > "+gitTestShellQuote(marker)+"\n", 0o700)
	entries = append(entries, sourceEntry{Path: ".gitattributes", Mode: 0o100644, Data: []byte("* filter=hostile text eol=crlf\n")}, sourceEntry{Path: "new/run", Mode: 0o100755, Data: []byte("raw\x00\r\n")})
	candidate, err := g.commit(t.Context(), base, entries)
	if err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "config", "i18n.commitEncoding", "ISO-8859-1")
	second, err := g.commit(t.Context(), base, entries)
	if err != nil || second != candidate {
		t.Fatalf("replay produced another commit: %q != %q: %v", second, candidate, err)
	}
	if parents := gitTestRun(t, repo, "show", "-s", "--format=%P", candidate); parents != base {
		t.Fatalf("candidate parents = %q", parents)
	}
	if identity := gitTestRun(t, repo, "show", "-s", "--format=%an|%ae|%cn|%ce|%at|%ct", candidate); identity != fmt.Sprintf("Oro Factory|factory@oro.invalid|Oro Factory|factory@oro.invalid|%d|%d", manifest.CreatedAt.Unix(), manifest.CreatedAt.Unix()) {
		t.Fatalf("unfrozen commit identity = %q", identity)
	}
	got, err := g.export(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(entries, func(a, b sourceEntry) int { return strings.Compare(a.Path, b.Path) })
	if !reflect.DeepEqual(got, entries) {
		t.Fatal("candidate tree changed validated entries")
	}
	if err := g.promote(t.Context(), base, candidate); err != nil {
		t.Fatal(err)
	}
	if actual := gitTestRun(t, repo, "rev-parse", "delivery"); actual != candidate {
		t.Fatal("candidate was not promoted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate filter/hook executed: %v", err)
	}
	empty, err := g.commit(t.Context(), candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if emptyEntries, err := g.export(t.Context(), empty); err != nil || len(emptyEntries) != 0 {
		t.Fatalf("empty root tree did not round trip: %v", err)
	}
	for _, call := range runtime.calls {
		for _, forbidden := range []string{"add", "checkout", "commit", "archive"} {
			if slices.Contains(call.Args, forbidden) {
				t.Fatalf("unsafe managed Git command: %v", call.Args)
			}
		}
	}
}

func testSourceGitInvalidEntries(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, nil)
	for _, entries := range [][]sourceEntry{
		{{Path: "../escape", Mode: 0o100644}},
		{{Path: ".Git/config", Mode: 0o100644}},
		{{Path: "a", Mode: 0o120000}},
		{{Path: "a", Mode: 0o100644}, {Path: "A", Mode: 0o100644}},
		{{Path: "a", Mode: 0o100644}, {Path: "a/b", Mode: 0o100644}},
		{{Path: strings.Repeat("p", 256), Mode: 0o100644}},
		{{Path: "big", Mode: 0o100644, Data: make([]byte, (4<<20)+1)}},
	} {
		if _, err := g.commit(t.Context(), base, entries); err == nil {
			t.Fatal("invalid entry vector accepted")
		}
	}
	if len(runtime.calls) != 0 {
		t.Fatal("invalid candidate reached host before full validation")
	}
}

func testSourceGitCAS(t *testing.T) {
	t.Helper()
	g, runtime, manifest, base := sourceGitFixture(t, nil, nil)
	candidate, err := g.commit(t.Context(), base, []sourceEntry{{Path: "candidate", Mode: 0o100644, Data: []byte("candidate")}})
	if err != nil {
		t.Fatal(err)
	}
	repo := manifest.Spec.Repository
	other := gitTestRun(t, repo, "commit-tree", gitTestRun(t, repo, "rev-parse", base+"^{tree}"), "-p", base, "-m", "external")
	runtime.before = func(call HostInvocation) {
		if slices.Contains(call.Args, "update-ref") {
			gitTestRun(t, repo, "update-ref", "refs/heads/delivery", other, base)
		}
	}
	if err := g.promote(t.Context(), base, candidate); err == nil {
		t.Fatal("stale CAS succeeded")
	}
	if actual := gitTestRun(t, repo, "rev-parse", "delivery"); actual != other {
		t.Fatal("CAS overwrote external advance")
	}
}

func testSourceGitOutput(t *testing.T) {
	t.Helper()
	for _, failure := range []string{"overflow", "truncated", "wrong-oid", "nonzero", "runtime"} {
		t.Run(failure, func(t *testing.T) {
			g, runtime, _, base := sourceGitFixture(t, nil, nil)
			runtime.alter = func(call HostInvocation, result HostResult) HostResult {
				switch failure {
				case "overflow":
					result.Stdout = make([]byte, call.OutputLimit+1)
				case "truncated":
					result.Stdout = nil
				case "wrong-oid":
					if len(result.Stdout) > 0 {
						result.Stdout[0] = 'z'
					}
				case "nonzero":
					result.ExitCode = 17
				}
				return result
			}
			if failure == "runtime" {
				runtime.failure = errors.New("retirement uncertain")
			}
			if _, err := g.export(t.Context(), base); err == nil {
				t.Fatal("invalid host result accepted")
			}
		})
	}
}

func testSourceGitBudget(t *testing.T) {
	t.Helper()
	g, runtime, _, _ := sourceGitFixture(t, nil, func(spec *ManagedSpec) { spec.Runtime.MaxHostInvocations = 1 })
	_, _ = g.target(t.Context())
	before := len(runtime.calls)
	if _, err := g.target(t.Context()); err == nil || len(runtime.calls) != before {
		t.Fatalf("exhausted budget allowed another effect: %v", err)
	}
}

func testSourceGitDeadline(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, nil)
	if _, err := g.export(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	if len(runtime.calls) < 2 {
		t.Fatal("fixture did not exercise a multi-command operation")
	}
	for _, call := range runtime.calls[1:] {
		if !call.Deadline.Equal(runtime.calls[0].Deadline) {
			t.Fatal("operation deadline refreshed between commands")
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(runtime.calls)
	if _, err := g.export(canceled, base); err == nil || len(runtime.calls) != before {
		t.Fatal("canceled operation reached host")
	}
}

func testSourceGitIdentity(t *testing.T) {
	t.Helper()
	g, runtime, manifest, base := sourceGitFixture(t, nil, nil)
	changed := manifest
	changed.PolicyHash = strings.Repeat("b", 64)
	if _, err := newManagedGitRepository(changed, 1, runtime.j, runtime); err == nil {
		t.Fatal("mismatched manifest accepted")
	}
	if _, _, err := runtime.j.beginGeneration(t.Context(), "replacement", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.export(t.Context(), base); err == nil || len(runtime.calls) != 0 {
		t.Fatal("stale owner reached host")
	}
}

func testSourceGitUncertain(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, nil)
	runtime.failure = errors.New("retirement uncertain")
	if _, err := g.export(t.Context(), base); err == nil {
		t.Fatal("uncertain invocation succeeded")
	}
	runtime.failure = nil
	before := len(runtime.calls)
	if _, err := g.export(t.Context(), base); err == nil || len(runtime.calls) != before {
		t.Fatal("uncertain runtime allowed more effects on the same repository")
	}
	// An ordinary nonzero exit has confirmed retirement and is not uncertainty.
	g, runtime, _, base = sourceGitFixture(t, nil, nil)
	runtime.alter = func(_ HostInvocation, result HostResult) HostResult { result.ExitCode = 17; return result }
	if _, err := g.export(t.Context(), base); err == nil {
		t.Fatal("nonzero command succeeded")
	}
	runtime.alter = nil
	if _, err := g.export(t.Context(), base); err != nil {
		t.Fatalf("ordinary failure poisoned retired runtime: %v", err)
	}
}

func testSourceGitAllocationFailure(t *testing.T) {
	t.Helper()
	g, runtime, _, base := sourceGitFixture(t, nil, func(spec *ManagedSpec) { spec.Runtime.MaxHostInvocations = 1 })
	if _, err := runtime.j.conn.ExecContext(t.Context(), "CREATE TRIGGER reject_git_scope BEFORE INSERT ON managed_scopes BEGIN SELECT RAISE(ABORT,'injected scope failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.export(t.Context(), base); err == nil || len(runtime.calls) != 0 {
		t.Fatal("failed scope reservation reached backend")
	}
	if _, err := runtime.j.conn.ExecContext(t.Context(), "DROP TRIGGER reject_git_scope"); err != nil {
		t.Fatal(err)
	}
	var spent int
	if err := runtime.j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='host'").Scan(&spent); err != nil || spent != 1 {
		t.Fatalf("failed scope lost durable spend: %d, %v", spent, err)
	}
	if _, err := g.export(t.Context(), base); err == nil || len(runtime.calls) != 0 {
		t.Fatal("failed scope refunded host budget")
	}
}

func testSourceGitRequestBounds(t *testing.T) {
	t.Helper()
	g, _, _, _ := sourceGitFixture(t, nil, nil)
	for _, call := range []HostInvocation{
		{OutputLimit: g.limits.HostOutputBytes, Stdin: make([]byte, g.limits.ArchiveBytes+1)},
		{OutputLimit: g.limits.HostOutputBytes + 1},
		{OutputLimit: 1, Args: []string{strings.Repeat("x", 1<<20)}},
		{OutputLimit: 1, Env: []string{"NUL=\x00"}},
	} {
		if err := validateManagedHostRequest(call, g.limits); err == nil {
			t.Fatal("oversized or invalid transport accepted")
		}
	}
}

func testSourceGitTarget(t *testing.T) {
	t.Helper()
	g, _, manifest, base := sourceGitFixture(t, nil, nil)
	if actual, err := g.target(t.Context()); err != nil || actual != base {
		t.Fatalf("managed target = %q, %v", actual, err)
	}
	gitTestRun(t, manifest.Spec.Repository, "worktree", "add", filepath.Join(t.TempDir(), "checkout"), manifest.Spec.TargetBranch)
	if _, err := g.target(t.Context()); err == nil {
		t.Fatal("checked-out target accepted")
	}
	if err := g.promote(t.Context(), base, base); err == nil {
		t.Fatal("checked-out target promoted")
	}
}
