package dispatcher //nolint:testpackage // verifies the internal audit lifecycle end-to-end

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestAuditSpawnMergePipeline(t *testing.T) {
	var _ func(*Dispatcher, context.Context) = (*Dispatcher).spawnAudit

	d, beads, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}

	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict: ops.VerdictRejected,
		Findings: []ops.Finding{{
			Severity:   ops.SevImportant,
			Category:   "correctness",
			Title:      "shared audit finding",
			Detail:     "the fixture needs a remediation",
			Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: "package fixture"}},
			Confidence: 50,
			Origin:     "pre_existing",
		}},
	})

	triggerAuditCycle(context.Background(), d)

	waitFor(t, func() bool {
		return len(createdCallsWithMetadata(beads, auditFindingMetadataKey, "")) == 1 &&
			eventCount(t, d.db, "audit_coverage") == 1
	}, 2*time.Second)

	created := createdCallsWithMetadata(beads, auditFindingMetadataKey, "")[0]
	if created.priority != 1 {
		t.Fatalf("audit finding priority = %d, want 1", created.priority)
	}
	if created.beadType != "task" {
		t.Fatalf("audit finding type = %q, want task", created.beadType)
	}
	if created.title != "shared audit finding" {
		t.Fatalf("audit finding title = %q", created.title)
	}
	if !strings.Contains(created.description, "wont-fix:") || !strings.Contains(created.description, "reopen") {
		t.Fatalf("audit finding description omitted suppression contract: %q", created.description)
	}
	roleCalls := createdCallsWithMetadata(beads, "meta_role", "audit")
	if len(roleCalls) != 1 || roleCalls[0].status != "closed" {
		t.Fatalf("audit role creates = %#v, want one atomically closed role bead", roleCalls)
	}
	assertAuditRoleJourney(t, beads, roleCalls[0].id, "audit_finding", "audit_coverage")
	if got := spawner.SpawnCount(); got != 6 {
		t.Fatalf("audit spawn calls = %d, want six sections", got)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit escalations = %d, want none", got)
	}
	var coveragePayload string
	if err := d.db.QueryRow(`SELECT payload FROM events WHERE type='audit_coverage' ORDER BY id DESC LIMIT 1`).Scan(&coveragePayload); err != nil {
		t.Fatalf("load audit coverage event: %v", err)
	}
	var coverage struct {
		CoveredSections []string `json:"covered_sections"`
		NotCovered      []string `json:"not_covered"`
	}
	if err := json.Unmarshal([]byte(coveragePayload), &coverage); err != nil {
		t.Fatalf("parse audit coverage event: %v", err)
	}
	wantCovered := []string{"code-quality", "tests-safety", "data-migrations", "security-static", "perf-patterns", "dx-deps-docs"}
	wantNotCovered := []string{"product-correctness-live", "reliability-injection", "integrations-live", "deploy-observability"}
	if !slices.Equal(coverage.CoveredSections, wantCovered) || !slices.Equal(coverage.NotCovered, wantNotCovered) {
		t.Fatalf("audit coverage = %#v, want covered=%#v not_covered=%#v", coverage, wantCovered, wantNotCovered)
	}

	d.mu.Lock()
	mergesSinceJanitor := d.mergesSinceJanitor
	janitorRunsSinceAudit := d.janitorRunsSinceAudit
	d.mu.Unlock()
	if mergesSinceJanitor != 0 || janitorRunsSinceAudit != 0 {
		t.Fatalf("audit altered counters: merges=%d janitors=%d", mergesSinceJanitor, janitorRunsSinceAudit)
	}

	t.Run("suppression matches janitor close semantics", func(t *testing.T) {
		finding := auditFixtureFinding()
		findingID := ops.FindingID("", finding)
		tests := []struct {
			name        string
			bead        *protocol.Bead
			wantCreated int
		}{
			{
				name: "open finding blocks duplicate filing",
				bead: auditFindingBead("open", "", findingID),
			},
			{
				name: "wont-fix close suppresses permanently",
				bead: auditFindingBead("closed", "wont-fix: intentional", findingID),
			},
			{
				name: "wont-fix prefix is case insensitive",
				bead: auditFindingBead("closed", "WONT-FIX: accepted risk", findingID),
			},
			{
				name:        "fixed close refiles when detected again",
				bead:        auditFindingBead("closed", "fixed", findingID),
				wantCreated: 1,
			},
			{
				name:        "reasonless close refiles when detected again",
				bead:        auditFindingBead("closed", "", findingID),
				wantCreated: 1,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assertAuditFindingCreateCount(t, finding, tc.bead, tc.wantCreated)
			})
		}

		t.Run("janitor wont-fix survives line drift", func(t *testing.T) {
			prior := auditFixtureFinding()
			prior.ID = ops.FindingID("", prior)
			incoming := prior
			incoming.Evidence = []ops.Evidence{{
				File: "audit.go", LineStart: 3, LineEnd: 3, Quote: "package fixture",
			}}
			incoming.ID = ops.FindingID("", incoming)
			assertAuditBucketSuppressed(t, incoming, prior)
		})
	})
}

func TestAuditSpawnAllSectionsFailedDoesNotEscalate(t *testing.T) {
	d, beads, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return auditFixtureRepo(t), "agent/audit", nil
	}
	spawner.spawnErr = errors.New("audit runtime unavailable")

	triggerAuditCycle(context.Background(), d)
	waitFor(t, func() bool {
		return eventCount(t, d.db, "audit_failed") == 1
	}, 10*time.Second)

	if created := len(createdCallsWithMetadata(beads, auditFindingMetadataKey, "")); created != 0 {
		t.Fatalf("created finding beads = %d, want none", created)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit escalations = %d, want none", got)
	}
	if got := eventCount(t, d.db, "audit_failed"); got != 1 {
		t.Fatalf("audit_failed notes = %d, want 1", got)
	}
	roleCalls := createdCallsWithMetadata(beads, "meta_role", "audit")
	if len(roleCalls) != 1 {
		t.Fatalf("audit role creates = %d, want 1", len(roleCalls))
	}
	assertAuditRoleJourney(t, beads, roleCalls[0].id, "note")
	d.mu.Lock()
	mergesSinceJanitor := d.mergesSinceJanitor
	janitorRunsSinceAudit := d.janitorRunsSinceAudit
	d.mu.Unlock()
	if mergesSinceJanitor != 0 || janitorRunsSinceAudit != 0 {
		t.Fatalf("failed audit counters: merges=%d janitors=%d, want reset", mergesSinceJanitor, janitorRunsSinceAudit)
	}
}

func TestAuditSpawnSerializesOverlappingRuns(t *testing.T) {
	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	spawner := newBlockingAuditSpawner(auditSectionOutput(t, ops.ReviewReport{Verdict: ops.VerdictApproved}))
	d.ops = ops.NewSpawner(spawner)

	firstDone := make(chan struct{})
	go func() {
		d.spawnAudit(context.Background())
		close(firstDone)
	}()
	waitFor(t, func() bool { return spawner.SpawnCount() == 4 }, time.Second)

	secondDone := make(chan struct{})
	go func() {
		d.spawnAudit(context.Background())
		close(secondDone)
	}()
	defer func() {
		spawner.Release()
		<-firstDone
		<-secondDone
	}()

	select {
	case <-spawner.fifthSpawn:
		t.Fatal("second audit spawned while first audit was still running")
	case <-time.After(250 * time.Millisecond):
	}

	spawner.Release()
	<-firstDone
	<-secondDone
}

func TestAuditSpawnRecordsJourneyAppendFailure(t *testing.T) {
	d, baseStore, worktrees, esc, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict:  ops.VerdictRejected,
		Findings: []ops.Finding{auditFixtureFinding()},
	})
	d.beads = &failingAuditJourneyStore{
		fakeBeadStore: baseStore,
		err:           errors.New("journey unavailable"),
	}

	d.spawnAudit(context.Background())

	if got := eventCount(t, d.db, "audit_finding_persist_failed"); got != 1 {
		t.Fatalf("audit_finding_persist_failed events = %d, want 1", got)
	}
	if got := len(createdCallsWithMetadata(baseStore, auditFindingMetadataKey, "")); got != 0 {
		t.Fatalf("findings filed without durable audit journey = %d, want 0", got)
	}
	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("audit journey failure escalations = %d, want 0", got)
	}
}

func triggerAuditCycle(ctx context.Context, d *Dispatcher) {
	d.cfg.JanitorEnabled = true
	d.cfg.JanitorInterval = 1
	d.cfg.JanitorIdleThreshold = 0
	d.cfg.AuditEnabled = true
	d.cfg.AuditEveryNJanitors = 5
	d.mergesSinceJanitor = 0
	d.janitorRunsSinceAudit = 4
	d.maybeTriggerJanitor(ctx)
}

func assertAuditFindingCreateCount(t *testing.T, finding ops.Finding, existing *protocol.Bead, want int) {
	t.Helper()
	d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict:  ops.VerdictRejected,
		Findings: []ops.Finding{finding},
	})
	beads.metadataMatches = []*protocol.Bead{existing}
	beads.journeys = make(map[string][]beadstore.JourneyEvent)
	priorFinding := finding
	priorFinding.ID, _ = existing.Metadata[auditFindingMetadataKey].(string)
	priorPayload, err := json.Marshal(priorFinding)
	if err != nil {
		t.Fatalf("marshal prior audit finding: %v", err)
	}
	beads.journeys["oro-new1"] = []beadstore.JourneyEvent{{
		Actor: auditRoleActor, Event: "audit_finding", Payload: string(priorPayload),
	}}

	d.spawnAudit(context.Background())

	created := len(createdCallsWithMetadata(beads, auditFindingMetadataKey, ""))
	if created != want {
		t.Fatalf("created beads = %d, want %d", created, want)
	}
}

func assertAuditBucketSuppressed(t *testing.T, incoming, prior ops.Finding) {
	t.Helper()
	d, beads, worktrees, _, _, spawner := newTestDispatcher(t)
	worktree := auditFixtureRepo(t)
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, "agent/audit", nil
	}
	spawner.verdict = auditSectionOutput(t, ops.ReviewReport{
		Verdict:  ops.VerdictRejected,
		Findings: []ops.Finding{incoming},
	})
	priorPayload, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal prior janitor finding: %v", err)
	}
	beads.metadataMatches = []*protocol.Bead{
		auditFindingBead("closed", "wont-fix: accepted janitor finding", prior.ID),
		{ID: "oro-janitor-role", Status: "closed", Metadata: map[string]any{"meta_role": "janitor"}},
	}
	beads.journeys = make(map[string][]beadstore.JourneyEvent)
	beads.journeys["oro-janitor-role"] = []beadstore.JourneyEvent{{
		Actor: "ops_janitor", Event: "janitor_finding", Payload: string(priorPayload),
	}}

	d.spawnAudit(context.Background())

	if got := len(createdCallsWithMetadata(beads, auditFindingMetadataKey, "")); got != 0 {
		t.Fatalf("line-drifted audit finding creates = %d, want cross-role suppression", got)
	}
}

func createdCallsWithMetadata(store *fakeBeadStore, key, value string) []createCall {
	store.mu.Lock()
	defer store.mu.Unlock()
	calls := make([]createCall, 0, len(store.created))
	for _, call := range store.created {
		got, ok := call.metadata[key]
		if ok && (value == "" || got == value) {
			calls = append(calls, call)
		}
	}
	return calls
}

func assertAuditRoleJourney(t *testing.T, store *fakeBeadStore, roleBeadID string, wantEvents ...string) {
	t.Helper()
	store.mu.Lock()
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	for _, want := range wantEvents {
		if !slices.ContainsFunc(journey, func(event beadstore.JourneyEvent) bool {
			return event.Actor == "ops_audit" && event.Event == want
		}) {
			t.Fatalf("audit role journey = %#v, want ops_audit %s", journey, want)
		}
	}
}

func auditFindingBead(status, closeReason, findingID string) *protocol.Bead {
	return &protocol.Bead{
		Status:      status,
		CloseReason: closeReason,
		Metadata: map[string]any{
			auditFindingMetadataKey: findingID,
		},
	}
}

func auditFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "audit.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write audit fixture: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "audit.go"},
		{"-c", "user.name=Oro Test", "-c", "user.email=oro@example.invalid", "commit", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if status := strings.TrimSpace(string(output)); status != "" {
		t.Fatalf("audit fixture is not a clean checkout: %s", status)
	}
	return repo
}

func auditSectionOutput(t *testing.T, report ops.ReviewReport) string {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal audit report: %v", err)
	}
	return "```json\n" + string(payload) + "\n```\nVERDICT: REJECTED\n"
}

func auditFixtureFinding() ops.Finding {
	return ops.Finding{
		Severity:   ops.SevImportant,
		Category:   "correctness",
		Title:      "shared audit finding",
		Detail:     "the fixture needs a remediation",
		Evidence:   []ops.Evidence{{File: "audit.go", LineStart: 1, LineEnd: 1, Quote: "package fixture"}},
		Confidence: 50,
		Origin:     "pre_existing",
	}
}

type failingAuditJourneyStore struct {
	*fakeBeadStore
	err error
}

func (s *failingAuditJourneyStore) AppendJourney(context.Context, string, beadstore.JourneyEvent) error {
	return s.err
}

type blockingAuditSpawner struct {
	mu         sync.Mutex
	output     string
	release    chan struct{}
	releaseOne sync.Once
	fifthSpawn chan struct{}
	fifthOne   sync.Once
	spawns     int
}

func newBlockingAuditSpawner(output string) *blockingAuditSpawner {
	return &blockingAuditSpawner{
		output:     output,
		release:    make(chan struct{}),
		fifthSpawn: make(chan struct{}),
	}
}

func (s *blockingAuditSpawner) Spawn(context.Context, string, string, string) (ops.Process, error) {
	s.mu.Lock()
	s.spawns++
	if s.spawns == 5 {
		s.fifthOne.Do(func() { close(s.fifthSpawn) })
	}
	s.mu.Unlock()
	return &blockingAuditProcess{release: s.release, output: s.output}, nil
}

func (s *blockingAuditSpawner) SpawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawns
}

func (s *blockingAuditSpawner) Release() {
	s.releaseOne.Do(func() { close(s.release) })
}

type blockingAuditProcess struct {
	release <-chan struct{}
	output  string
}

func (p *blockingAuditProcess) Wait() error {
	<-p.release
	return nil
}

func (p *blockingAuditProcess) Kill() error             { return nil }
func (p *blockingAuditProcess) Output() (string, error) { return p.output, nil }
func (p *blockingAuditProcess) LastOutputAt() time.Time { return time.Time{} }
