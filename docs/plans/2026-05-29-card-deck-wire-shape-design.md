# Card Deck Wire Shape Design

Date: 2026-05-29
Status: Validated by deep premortem and adversarial review

## Goal

Prevent large card stores from making worker `ASSIGN` messages too large or too
slow to write by correcting the card context contract:

- Deck entries sent in `ASSIGN` are lightweight summaries only.
- Only inlined entries carry `body_full`.
- The dispatcher still applies an assignment-size budget as a defense in depth.
- Workers can still discover card IDs from deck view and fetch deeper content
  through the supported card-detail path.

The motivating production shape is a card deck with 3,610 active cards and about
650 KB of summaries before JSON overhead. The current implementation serializes
`BodyFull` on deck entries too, so payload size grows with full card bodies even
though the worker deck prompt does not render them.

## Non-Goals

- Do not raise `protocol.MaxMessageSize`.
- Do not rely on socket write deadlines as the sizing mechanism.
- Do not remove progressive disclosure.
- Do not redesign card scoring, promotion, decay, or retirement.
- Do not introduce a new remote card service.

## Source Research

- `pkg/cards/cards.go`
  - `CardSummary` is documented as the deck-view representation, but it includes
    `BodyFull`.
  - `RelevantCards.Deck` is documented as `body_summary only, all relevant`.
  - `RelevantCards.Inlined` is documented as `body_full inlined, fits within
    MaxTokens`.
- `pkg/cards/store.go`
  - `SQLiteCardStore.Relevant` reads all non-retired cards, filters by score,
    sorts by score, and returns `Deck: toSummaries(candidates)`.
  - `toSummary` copies `BodyFull`, so every deck entry carries full text.
  - `readTxImpl.Relevant` repeats the same return shape.
- `pkg/beadstore/testfake.go`
  - The fake cards transaction mirrors the same shape and copies `BodyFull` into
    every deck entry.
- `pkg/dispatcher/assign_payload.go`
  - `buildCardContext` requests card context with `MaxTokens: 2000`.
  - Current worktree changes add JSON-size trimming to the dispatcher. This is a
    useful guard, but it does not fix the underlying type/contract mismatch.
- `pkg/worker/prompt.go`
  - The worker prompt uses `BodyFull` only for `Cards.Inlined`.
  - Deck view renders only type, title, score, and ID, then tells the worker to
    fetch details on demand.
- `pkg/protocol/message.go`
  - `MaxMessageSize` is 1 MB, and both dispatcher and worker scanner buffers are
    configured around that limit.
- `docs/plans/2026-04-28-oro-harness-architecture-spec.md`
  - Section 5.5 says `body_summary` renders in the Cards section, `body_full`
    renders only when there is prompt budget, and `body_deep` is fetched on
    demand.
  - The spec's `RelevantCards` shape says Deck is `body_summary only`.
- `docs/plans/2026-05-28-readme-refresh-plan.md`
  - Notes that the prompt currently tells workers to use `oro cards show <id>`,
    but the public cards command group is migration/maintenance only. This is a
    separate but relevant dependency for deep-card fetch reliability.
- `cmd/oro/cmd_cards.go`
  - Registers `import-from-memory`, `check-drift`, and
    `memory-retirement-check`; it does not register `show`.
- `cmd/oro/cmd_current.go`, `cmd/oro/cmd_resume.go`, and
  `cmd/oro/cmd_handoff.go`
  - Each consumes `RelevantCards.Deck` for user-facing context renders.
  - `cardSummaryFromSummary` currently accepts `cards.CardSummary`, so splitting
    deck and inline types affects these CLI paths too.

No external references are needed; this is an internal protocol and data-shape
correction.

## Current Failure

The intended flow is progressive disclosure:

1. Dispatcher queries relevant cards.
2. Dispatcher sends a bounded deck plus a small inlined set in `ASSIGN`.
3. Worker renders full bodies only for inlined cards.
4. Worker uses card IDs from deck view to fetch deeper details on demand.

The current flow violates step 2. `CardSummary` is used for both deck and inline
records, and it contains `BodyFull`. That means the deck is "summary-only" in the
prompt but not on the wire.

This creates three failure modes:

- A large active deck can exceed the practical worker socket write window before
  the worker can read the assignment.
- JSON size can approach or exceed `protocol.MaxMessageSize` despite code-search
  truncation and worker-program truncation.
- Dispatcher-level trimming drops otherwise-useful cards because the payload is
  carrying unused full bodies.

## Design

### 1. Split Deck And Inline Wire Shapes

Introduce explicit types in `pkg/cards`:

```go
type DeckCard struct {
    ID          string
    Type        CardType
    Title       string
    BodySummary string
    Score       float64
    Tags        []string
}

type InlinedCard struct {
    ID          string
    Type        CardType
    Title       string
    BodySummary string
    BodyFull    string
    Score       float64
    Tags        []string
}

type RelevantCards struct {
    Deck    []DeckCard
    Inlined []InlinedCard
}
```

`CardSummary` should either be removed or kept temporarily as a private/internal
compatibility alias during the migration. The public contract should stop using a
single type whose fields mean different things depending on which slice contains
it.

Implementation choice for this epic: remove `CardSummary` from the public
`RelevantCards` contract and migrate callers to `DeckCard` / `InlinedCard`
directly. Do not keep a public compatibility alias in `pkg/cards`; compile
failures should expose every caller that still assumes deck cards can carry
`BodyFull`.

Decision premortem:

- Tiger: Changing `RelevantCards` breaks tests and fakes in multiple packages.
  Mitigation: migrate all compile failures in one task; do not add JSON aliases
  that preserve the ambiguous shape.
- Paper tiger: Worker prompt needs `BodyFull` for deck entries. It does not; the
  prompt only reads `BodyFull` from `Inlined`.
- Elephant: Deep fetch is currently underspecified because `oro cards show <id>`
  does not exist as a user-facing command. Include it as a separate task because
  the worker prompt already relies on that command for progressive disclosure.

### 2. Populate Deck Without BodyFull At The Store Boundary

Change `SQLiteCardStore.Relevant`, `readTxImpl.Relevant`, and the beadstore fake
to produce:

- `Deck`: all selected deck entries as `DeckCard`, no `BodyFull`.
- `Inlined`: only budgeted entries as `InlinedCard`, with `BodyFull`.

The SQL query can still select `body_full` initially because inline-budget
calculation needs it. A later optimization may split selection or defer full-body
loading, but that is not required for the payload fix.

Decision premortem:

- Tiger: If only the SQLite implementation changes, tests using the fake still
  pass while production shape differs. Mitigation: update both `pkg/cards` and
  `pkg/beadstore/testfake.go`.
- Paper tiger: Keeping `body_full` in the SQL row wastes DB read bandwidth. True,
  but the immediate incident is wire/socket size. DB query optimization can wait.

### 3. Keep Dispatcher Assignment Budgets

Keep the dispatcher-side JSON budget introduced in the worktree, but adjust it
to the new types:

- Deck budget applies to `[]cards.DeckCard`.
- Inline budget applies to `[]cards.InlinedCard`.
- The budget function should be generic or duplicated with exact types, not
  force a return to the old ambiguous `CardSummary`.

This budget is a safety net for pathological card counts, not the main
correctness mechanism. With `BodyFull` removed from deck entries, trimming should
drop far fewer cards for the same byte cap.

Decision premortem:

- Tiger: A fixed deck byte budget can still truncate all useful cards if the top
  cards have very large summaries or tag lists. Mitigation: tests should assert
  top-order preservation and valid under-limit JSON; card hygiene can separately
  enforce short summaries.
- Paper tiger: Generic trimming might obscure behavior. This is local,
  deterministic, and testable by marshaled JSON byte length.

### 4. Preserve Protocol Compatibility Where It Matters

This is an internal dispatcher-worker protocol. There is no requirement to accept
old workers indefinitely, but rolling upgrades can briefly mix binaries. The
least complex compatibility story is:

- New dispatcher sends `deck` entries without `body_full`.
- New worker reads the new typed shape.
- If old persisted JSON fixtures include `body_full` inside deck entries, Go's
  JSON decoder ignores unknown fields when decoding to `DeckCard`.
- If an old dispatcher sends deck entries with `body_full`, a new worker ignores
  that field on deck entries because it decodes into `DeckCard`.

No schema migration is needed. Stored cards still retain `body_full`; only the
assignment context shape changes.

### 5. Make The Deep-Fetch Path Real

Add a read-only `oro cards show <id>` command that prints a card's full body.
This closes the progressive-disclosure loop the worker prompt already advertises.

Minimum behavior:

- `oro cards show <id>` prints type, title, summary, full body, score, and tags.
- `oro cards show <id> --json` emits the same data as JSON.
- Missing cards return a non-zero exit with a clear not-found error.

This task is not a new remote service and does not change card storage. It only
exposes the existing `cards.Store.Show` path through the CLI that workers are
already instructed to use.

Decision premortem:

- Tiger: If `BodyFull` is removed from deck ASSIGN payloads before `cards show`
  exists, workers can discover deck IDs but cannot fetch the details the prompt
  tells them to fetch. Mitigation: make `cards show` a dependency of integrated
  acceptance.
- Paper tiger: Adding a CLI command expands scope. The command is read-only and
  sits on the existing cards store, so it is the smallest way to make the
  advertised deep-fetch path true.

### 6. Tests And Acceptance

Add or update focused tests:

- `pkg/cards/store_test.go`
  - `TestRelevantDeckOmitsBodyFull`
  - Creates a deck-only card with `BodyFull: "DECK_FULL_BODY_SENTINEL"` and an
    inline card with `BodyFull: "INLINE_FULL_BODY_SENTINEL"`.
  - Calls `Relevant`.
  - Marshals `result.Deck`.
  - Asserts the JSON does not contain the full-body sentinel and `Inlined` still
    does when token budget allows.
- `pkg/beadstore/testfake_test.go`
  - Mirrors the same assertion for the fake transaction path.
- `pkg/protocol/message_test.go`
  - Updates card round-trip expectations so deck entries have no `BodyFull` and
    inline entries still do.
- `pkg/worker/prompt_cards_section_test.go`
  - Confirms deck prompt rendering still works with `DeckCard`.
  - Confirms deck-only cards render `BodySummary` but not `BodyFull`.
  - Confirms inline cards still render when `Deck` is empty but `Inlined` is not.
- `cmd/oro/cmd_cards_test.go`
  - Adds `TestCardsShowPrintsFullBody`.
- `cmd/oro/cmd_current_test.go`
  - Adds `TestCurrentRendersDeckCardSummariesWithoutFullBody`.
- `cmd/oro/cmd_resume_test.go`
  - Adds `TestResumeRendersDeckCardSummaryWithoutFullBody`.
- `cmd/oro/cmd_handoff_test.go`
  - Adds `TestHandoffRendersDeckCardSummariesWithoutFullBody`.
- `pkg/dispatcher/assign_payload_test.go`
  - Keeps the large-deck ASSIGN-size regression.
  - Adds sentinel assertions that the marshaled `ASSIGN` does not contain
    `"DECK_ONLY_FULL_BODY_SENTINEL"` and does contain
    `"INLINE_ONLY_FULL_BODY_SENTINEL"`.

Epic acceptance command:

```bash
test "$(git branch --show-current)" = main && go test ./pkg/cards ./pkg/beadstore ./pkg/protocol ./pkg/worker ./pkg/dispatcher ./cmd/oro
```

Assert:

- All tests pass.
- Large ASSIGN payload remains below `protocol.MaxMessageSize`.
- Marshaled deck JSON does not contain deck `BodyFull` sentinel text.
- Inline card JSON still contains inline `BodyFull` sentinel text.
- Worker deck view includes card summaries but not full bodies.
- `oro cards show <id>` can fetch the full body for a deck card.
- `current`, `resume`, and `handoff` render deck summaries without leaking
  full bodies.

## Deep Premortem

```yaml
premortem:
  mode: deep
  context: "Card deck wire-shape design"

  tigers:
    - risk: "The spec removes BodyFull from deck payloads while the advertised deep-fetch command does not exist."
      severity: high
      evidence: "pkg/worker/prompt.go:73 tells workers to run `oro cards show <id>`; cmd/oro/cmd_cards.go:12-14 registers only import-from-memory, check-drift, and memory-retirement-check."
      mitigation: "Add a required `oro cards show <id>` task and make integrated acceptance depend on it."
    - risk: "Worker prompt can drop inline cards when Deck is empty but Inlined is non-empty."
      severity: high
      evidence: "pkg/worker/prompt.go:45-47 returns early when len(rc.Deck)==0 before iterating rc.Inlined."
      mitigation: "Task 4 must change the empty-cards condition to require both Deck and Inlined to be empty."
    - risk: "Deck summaries may still be sent but not shown to workers."
      severity: medium
      evidence: "pkg/worker/prompt.go:70-71 renders type, title, score, and id, but not BodySummary; the harness spec says body_summary always renders."
      mitigation: "Task 4 must render BodySummary for deck-only entries and test that full bodies are not rendered there."
    - risk: "A sentinel payload-size test can pass for the wrong reason if the same full-body sentinel appears in both Deck and Inlined."
      severity: medium
      evidence: "Current tests often build Deck by appending Inlined, so a full-body string may legitimately appear through Inlined."
      mitigation: "Assignment and store tests must use distinct deck-only and inline-only sentinels."

  elephants:
    - risk: "This is no longer only a wire-shape patch if progressive disclosure is part of the promise; it also needs the CLI detail path."

  paper_tigers:
    - risk: "Selecting body_full in SQL remains wasteful after the wire fix."
      reason: "The immediate failure is ASSIGN wire/socket size. Inline budgeting still needs body_full, and DB query optimization can be separate."
    - risk: "Changing public card types causes many compile failures."
      reason: "That is expected and desirable here; compile failures expose every contract user that must migrate."
```

## Adversarial Review

The first fresh-context adversarial review returned `FAIL`.

Reviewer summary:

```yaml
verdict: FAIL
reviewer_note: "The wire-shape fix is mostly covered, but the task graph leaves the promised deep card-detail path and cmd/oro consumers uncovered."
critical_gaps:
  - "Workers can fetch deeper content via supported card-detail path from deck IDs"
  - "Non-dispatcher CLI consumers of RelevantCards.Deck compile and preserve summary-only JSON/text rendering"
  - "Epic acceptance should make the main-branch requirement explicit"
```

Folded-in fixes:

- `oro cards show <id>` is now a required task, not a follow-up.
- The epic acceptance command now includes `./cmd/oro` and an explicit main-branch
  check.
- `current`, `resume`, and `handoff` deck-renderer migrations are now explicit
  tasks with their own tests.

Second fresh-context adversarial review:

```yaml
verdict: PASS
reviewer_note: "The revised task graph covers the prior deep-fetch and CLI-renderer gaps; I found no structural path where all listed tasks pass while the feature remains broken."
acceptance_test:
  adequate: true
traceability:
  covered: 11
  gaps: 0
wiring_gaps: []
negative_space: []
```

## Task Graph

### Task 1: Split card deck and inline types

Acceptance:

```text
Test: pkg/protocol/message_test.go:TestAssignPayloadCardsContextRoundTrip
Cmd: go test ./pkg/protocol -run TestAssignPayloadCardsContextRoundTrip
Assert: deck entries round-trip without BodyFull; inline entries round-trip with BodyFull.
Read: pkg/cards/cards.go:CardSummary, pkg/cards/cards.go:RelevantCards, pkg/protocol/message.go:AssignPayload
Signature: type DeckCard struct { ID string; Type CardType; Title string; BodySummary string; Score float64; Tags []string }; type InlinedCard struct { ID string; Type CardType; Title string; BodySummary string; BodyFull string; Score float64; Tags []string }; type RelevantCards struct { Deck []DeckCard; Inlined []InlinedCard }
Edges: DeckCard has no BodyFull field; unknown JSON fields on deck entries are ignored by decoder.
```

Depends on: none.

### Task 2: Populate deck without full bodies in card stores

Acceptance:

```text
Test: pkg/cards/store_test.go:TestRelevantDeckOmitsBodyFull
Cmd: go test ./pkg/cards -run TestRelevantDeckOmitsBodyFull
Assert: marshaled Relevant().Deck excludes "DECK_FULL_BODY_SENTINEL"; marshaled Relevant().Inlined includes "INLINE_FULL_BODY_SENTINEL" when MaxTokens allows it.
Read: pkg/cards/store.go:Relevant, pkg/cards/store.go:toSummary, pkg/cards/store.go:buildInlined, pkg/cards/store.go:readTxImpl.Relevant
Signature: func toDeckCard(c Card) DeckCard; func toInlinedCard(c Card) InlinedCard
Edges: MaxTokens <= 0 -> Inlined nil/empty; Deck still excludes BodyFull.
```

Depends on: Task 1.

### Task 3: Keep fake card store contract in sync

Acceptance:

```text
Test: pkg/beadstore/testfake_test.go:TestCardsRelevantDeckOmitsBodyFull
Cmd: go test ./pkg/beadstore -run TestCardsRelevantDeckOmitsBodyFull
Assert: fake Cards().Relevant matches SQLite wire shape: marshaled Deck excludes "DECK_FULL_BODY_SENTINEL"; marshaled Inlined includes "INLINE_FULL_BODY_SENTINEL" when MaxTokens allows it.
Read: pkg/beadstore/testfake.go:fakeCardsReadTx.Relevant, pkg/beadstore/testfake.go:toFakeCardSummary
Signature: func toFakeDeckCard(c cards.Card) cards.DeckCard; func toFakeInlinedCard(c cards.Card) cards.InlinedCard
Edges: retired cards excluded; low-score filtering unchanged.
```

Depends on: Task 1.

### Task 4: Update worker prompt rendering to typed slices

Acceptance:

```text
Test: pkg/worker/prompt_cards_section_test.go:TestCardsSectionProgressiveDisclosure
Cmd: go test ./pkg/worker -run TestCardsSectionProgressiveDisclosure
Assert: prompt contains "INLINE_FULL_BODY_SENTINEL" for an inline card; contains deck-only id "card-deck-02" and summary "DECK_SUMMARY_SENTINEL"; does not contain "DECK_FULL_BODY_SENTINEL"; inline-only cards still render when Deck is empty.
Read: pkg/worker/prompt.go:cardsBody, pkg/worker/prompt_cards_section_test.go
Edges: empty Deck and empty Inlined -> "No relevant cards"; non-empty Inlined with empty Deck still renders inline cards; inlined IDs are not duplicated in deck view.
```

Depends on: Task 1.

### Task 5: Apply assignment card byte budgets to new types

Acceptance:

```text
Test: pkg/dispatcher/assign_payload_test.go:TestBuildCardContextKeepsAssignPayloadUnderProtocolLimit
Cmd: go test ./pkg/dispatcher -run TestBuildCardContextKeepsAssignPayloadUnderProtocolLimit
Assert: large active deck is capped below protocol.MaxMessageSize, preserves the first/top card, marshaled ASSIGN excludes "DECK_ONLY_FULL_BODY_SENTINEL", and marshaled ASSIGN includes "INLINE_ONLY_FULL_BODY_SENTINEL".
Read: pkg/dispatcher/assign_payload.go:buildCardContext, pkg/dispatcher/assign_payload.go:trimAssignmentCardContext, pkg/protocol/message.go:MaxMessageSize
Signature: func trimDeckCardsByJSONSize([]cards.DeckCard, int) []cards.DeckCard; func trimInlinedCardsByJSONSize([]cards.InlinedCard, int) []cards.InlinedCard
Edges: maxSize <= 0 -> nil; marshal error -> stop retaining additional entries.
```

Depends on: Tasks 1, 2, 3.

### Task 6: Add cards show command

Acceptance:

```text
Test: cmd/oro/cmd_cards_test.go:TestCardsShowPrintsFullBody
Cmd: go test ./cmd/oro -run TestCardsShowPrintsFullBody
Assert: `oro cards show card-show-1` prints title "Show Card Title", summary "SHOW_SUMMARY_SENTINEL", full body "SHOW_FULL_BODY_SENTINEL", score, and tag "show-tag"; `oro cards show card-show-1 --json` emits valid JSON with body_full="SHOW_FULL_BODY_SENTINEL"; `oro cards show missing-card` exits non-zero.
Read: cmd/oro/cmd_cards.go:newCardsCmd, pkg/cards/store.go:Show, pkg/cards/cards.go:Card
Signature: func newCardsShowCmd() *cobra.Command
Edges: missing id -> usage error; unknown id -> non-zero not-found error; --json -> valid JSON object.
```

Depends on: none.

### Task 7: Migrate current deck renderer

Acceptance:

```text
Test: cmd/oro/cmd_current_test.go:TestCurrentRendersDeckCardSummariesWithoutFullBody
Cmd: go test ./cmd/oro -run TestCurrentRendersDeckCardSummariesWithoutFullBody
Assert: `oro current --format json` cards include id "card-current-deck", title "Current Deck Card", body_summary "CURRENT_SUMMARY_SENTINEL", score, and tag "current-tag"; output does not contain "body_full" or "CURRENT_FULL_BODY_SENTINEL".
Read: cmd/oro/cmd_current.go:buildCurrentView, cmd/oro/cmd_current.go:cardSummaryFromSummary, cmd/oro/cmd_current_test.go:TestCurrentRendersInProgressJourneyAndCards
Signature: func cardSummaryFromDeckCard(c cards.DeckCard) cardSummaryJSON
Edges: duplicate deck IDs are still de-duped; nil Cards() still renders no cards.
```

Depends on: Task 1.

### Task 8: Migrate resume deck renderer

Acceptance:

```text
Test: cmd/oro/cmd_resume_test.go:TestResumeRendersDeckCardSummaryWithoutFullBody
Cmd: go test ./cmd/oro -run TestResumeRendersDeckCardSummaryWithoutFullBody
Assert: `oro resume bead-resume-1` renders linked card title "Resume Deck Card" and summary "RESUME_SUMMARY_SENTINEL"; output does not contain "body_full" or "RESUME_FULL_BODY_SENTINEL".
Read: cmd/oro/cmd_resume.go:runResume, cmd/oro/cmd_resume.go:renderResumeText, cmd/oro/cmd_current.go:cardSummaryFromDeckCard, cmd/oro/cmd_resume_test.go:TestResumeDropsIntoBeadContext
Edges: missing bead behavior unchanged; one WithReadTx span preserved.
```

Depends on: Task 7.

### Task 9: Migrate handoff deck renderer

Acceptance:

```text
Test: cmd/oro/cmd_handoff_test.go:TestHandoffRendersDeckCardSummariesWithoutFullBody
Cmd: go test ./cmd/oro -run TestHandoffRendersDeckCardSummariesWithoutFullBody
Assert: `oro handoff --since 1h` JSON cards include id "card-handoff-deck", title "Handoff Deck Card", body_summary "HANDOFF_SUMMARY_SENTINEL", score, and tag "handoff-tag"; output does not contain "body_full" or "HANDOFF_FULL_BODY_SENTINEL".
Read: cmd/oro/cmd_handoff.go:buildHandoffView, cmd/oro/cmd_current.go:cardSummaryFromDeckCard, cmd/oro/cmd_handoff_test.go:TestHandoffScopedToSessionWindow
Edges: duplicate deck IDs are still de-duped; session window filtering unchanged.
```

Depends on: Task 7.

### Task 10: Verify integrated assignment payload shape

Acceptance:

```text
Test: pkg/dispatcher/dispatcher_test.go:TestAssignPayloadCardsUseSummaryOnlyDeck
Cmd: go test ./pkg/dispatcher -run TestAssignPayloadCardsUseSummaryOnlyDeck
Assert: real ASSIGN received by a worker includes deck id "card-dispatcher-deck" and summary "DISPATCHER_DECK_SUMMARY_SENTINEL", excludes "DISPATCHER_DECK_FULL_BODY_SENTINEL", includes inline full body "DISPATCHER_INLINE_FULL_BODY_SENTINEL", and marshaled message length is < protocol.MaxMessageSize.
Read: pkg/dispatcher/dispatcher.go:tryAssign, pkg/dispatcher/assign_payload.go:buildAssignPayload, pkg/dispatcher/worker_pool.go:sendToWorker
Edges: cardStore nil -> empty cards; Relevant error -> assignment still sends with empty cards and logs card_context_failed.
```

Depends on: Tasks 2, 4, 5, 6, 7, 8, 9.

## Follow-Up

After the wire-shape fix lands, a later optimization can avoid selecting
`body_full` for every candidate in `Relevant` by loading full bodies only for
the inline-budgeted subset. That is not required to fix the ASSIGN payload size.
