package factory //nolint:testpackage // Exercise raw Git objects and the private managed ownership seam.

import (
	"bytes"
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

func TestManagedGitAuthority(t *testing.T) {
	t.Run("budget precedes effects and never refills", testSourceGitBudget)
	t.Run("one immutable operation deadline", testSourceGitDeadline)
	t.Run("stale generation and identity rejected", testSourceGitIdentity)
	t.Run("uncertain runtime stops this repository", testSourceGitUncertain)
	t.Run("scope insert failure cannot refund budget", testSourceGitAllocationFailure)
	t.Run("request bounds precede backend effects", testSourceGitRequestBounds)
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
