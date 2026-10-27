# Restricted Terra Helper Implementation Contract

> **For Codex:** Use executing-plans and the assigned tracked task to implement only its reviewed scope.

**Goal:** Connect the managed controller to a frozen Terra helper, restricted candidate tools, and positive candidate retirement; subsequently qualify real Astra architecture, Terra engineering and independent exact-SHA acceptance/review.

**Architecture:** One frozen helper process owns one non-resumable model invocation. Its Responses turns share that invocation's original deadline, financial allowance and bounded transcript. The controller alone authorizes container effects through an authenticated private Unix channel. There is no API broker, generic provider layer or controller-owned HTTP bypass.

**Baseline:** factory core Go `8cb36db8`, published checkpoint `856085c1`. This contract supersedes the historical Claude adapter prescription, not its isolation/budget requirements. Live API transport is unavailable until pricing (including token-count requests), credential provisioning and the real tool boundary are qualified. Recording tests do not qualify live delivery.

## Shared types and ownership

`pkg/factory/model_terra_protocol.go` owns the following types. Both the registered CLI helper and the core producer import this one schema; do not duplicate private wire structs. No fields may be added implicitly by another leaf.

```go
type ModelAdapterConfig struct {
    Protocol       string `json:"protocol"`
    CredentialPath string `json:"credential_path"`
    HelperSHA256   string `json:"helper_sha256"`
}

type ModelRequest struct {
    Version                 int                     `json:"version"`
    StateRoot               string                  `json:"state_root"`
    ObjectiveID             string                  `json:"objective_id"`
    Invocation              ScopeHandle             `json:"invocation"`
    CandidateID             string                  `json:"candidate_id"`
    CandidateScope          ScopeHandle             `json:"candidate_scope"`
    CandidateBindingSHA256  string                  `json:"candidate_binding_sha256"`
    Runtime                 ManagedRuntimeReference `json:"runtime"`
    Adapter                 ModelAdapterConfig      `json:"adapter"`
    Role                    string                  `json:"role"`
    Model                   string                  `json:"model"`
    ExecutionDeadline       time.Time               `json:"execution_deadline"`
    MaxTurns                int                     `json:"max_turns"`
    InvocationLiabilityCents int64                  `json:"invocation_liability_cents"`
    InputTokenCeiling       int                     `json:"input_token_ceiling"`
    OutputTokenCeiling      int                     `json:"output_token_ceiling"`
    TranscriptByteLimit    int64                   `json:"transcript_byte_limit"`
    ToolSocket              string                  `json:"tool_socket"`
    Prompt                  string                  `json:"prompt"`
}

type ModelTurnRecord struct {
    Turn              int      `json:"turn"`
    CountDispatched   bool     `json:"count_dispatched"`
    ModelDispatched   bool     `json:"model_dispatched"`
    ResponseID        string   `json:"response_id"`
    ObservedModel     string   `json:"observed_model"`
    Outcome           string   `json:"outcome"`
    InputTokens       int64    `json:"input_tokens"`
    OutputTokens      int64    `json:"output_tokens"`
    LiabilityCents    int64    `json:"liability_cents"`
    ToolCallIDs       []string `json:"tool_call_ids"`
}

type ModelResult struct {
    Version             int               `json:"version"`
    Invocation          ScopeHandle       `json:"invocation"`
    RequestSHA256       string            `json:"request_sha256"`
    Outcome             string            `json:"outcome"`
    TurnsDispatched     int               `json:"turns_dispatched"`
    LiabilitySpentCents int64             `json:"liability_spent_cents"`
    Turns               []ModelTurnRecord `json:"turns"`
    FinalText           string            `json:"final_text"`
    ErrorCode           string            `json:"error_code"`
}
```

Codecs: `EncodeModelRequest(ModelRequest) ([]byte,error)`, `DecodeModelRequest([]byte) (ModelRequest,error)`, package-private `encodeModelResult(ModelResult) ([]byte,error)` and `decodeModelResult([]byte) (ModelResult,error)`. Result codecs remain private until a genuine cross-package caller requires export; loop and backend consumers belong to `pkg/factory`, and tests alone do not justify a public API. Encoders validate first. Decoders enforce the raw JSON rules below, validate and require equality to canonical encoding before any trusted consumption or effects. Temporary typed decoding followed by exact canonical-byte rejection is permitted; normalized nulls, duplicates, aliases or unknown fields must never reach trusted use. Result/request binding and durable authority are separate mandatory consumer checks; decoding never grants work.

`CandidateID` is exactly the candidate row's `workspace_id`, not a Docker name. `CandidateScope` equals the durable candidate container handle. The binding digest is SHA-256 of the existing canonical `json.Marshal` of that candidate's persisted `Binding`, not an independently normalized or expanded label map. The intent still binds source/keeper identity through existing candidate validation. Invocation is the unique host-kind scope backed by the atomic model reservation; it is not the controller scope. All root/objective/runtime/candidate values must match committed authority, not merely pass shape checks.

For the first worker implementation, `Role == "worker"`, `Model == "gpt-6.1-terra"`, and Model equals the admitted `RuntimePolicy.Model`. Protocol is exactly `oro-terra-responses-v1`. Astra role extension is owned by `oro-mf-astra-plan`/`oro-mf-model-repair`; unknown roles/models currently fail before effects. That later change must explicitly bind its role/model mapping to frozen policy/helper identity and retain separate invocation allowances.

## Optional runtime configuration and executable identity

Append `ModelHelper *factory.ModelAdapterConfig` with JSON tag `model_helper,omitempty` to the CLI runtime struct. The trusted core reader projection carries the same optional typed value. Append, rather than reorder, existing runtime fields. A nil field is omitted and preserves historical serialized bytes/digests. Only complete absence means disabled: explicit null, empty object, empty required value, duplicate key or case alias is invalid. The existing outer version remains unchanged for this optional extension; the helper protocol is independently versioned.

Optional-config equality is by presence and complete value, never pointer address. Entry owns both existing reread consumers: `pkg/factory/managed_docker.go:managedDockerVolumes.ready` and `cmd/oro/cmd_factory_docker_cleanup_run.go:verifyFactoryDockerCleanupConfig`, as well as an audit of all runtime/config equality sites and projections. Add `EqualModelAdapterConfig(a, b *ModelAdapterConfig) bool` in the shared protocol file: both nil equal, exactly one nil unequal, otherwise `*a == *b`. At each complete-config comparison, compare optional values with this helper, then compare value copies with their optional pointers nilled; retain every other existing field comparison. Do not omit the adapter from identity or use reflection/serialization to hide missing fields. Independently decode identical artifacts twice (distinct nonnil pointers): ready and the actual authorized cleanup runner must succeed. Changing each adapter field or presence must fail before dispatch; absent historical config remains equal. Entry owns the two additional source sites and necessary existing cleanup-test updates under its approved atomic-plumbing scope exception.

Public `factory start --model-credential-file <absolute-path>` opts in. Publisher input/intent bind that nonsecret reference and `Protocol`; the publisher fills `HelperSHA256` from the already frozen Oro identity. No supplied helper hash may override the actual bytes. Omitted flag means no helper config. Active replay compares requested optional config with the committed artifact; changed inputs fail without republishing or reminting a runtime digest. Historical absence stays readable and adapter-unavailable.

The admitted `ModelExecutable` must explicitly point to the intended Oro helper build. Admission still freezes those configured bytes into `<root>/<id>/model`. Core dispatch requires its manifest program digest to equal `ModelHelper.HelperSHA256` and the runtime's pinned Oro SHA. It invokes that model copy, not `OroPath` as a substitute. The registered helper checks its own executable path/identity against that exact frozen model copy and bound protocol. A configured Codex CLI or different executable is not silently replaced.

`CredentialPath` is a nonempty clean absolute path, valid UTF-8, at most 4096 bytes, without NUL, outside the repository and factory state/artifact roots. Metadata publication/replay validates the reference without opening credential contents or requiring the credential to remain present. At actual qualified invocation only, the helper requires an owned private regular file, mode0600, single link, no symlink components, size1..4096 bytes; read once through verified file identity. Contents are one nonempty ASCII bearer token with no whitespace/control bytes; do not normalize or echo invalid values. Secret bytes never enter argv, environment, stdin, runtime JSON, prompt, tool RPC, transcript, diagnostics or error-body logging. No existing Codex/auth/key files are imported or inspected.

Helper argv is exactly `factory _model-terra --runtime-config <path> --runtime-config-sha256 <sha>`, with canonical ModelRequest on stdin. Environment is the existing fixed minimal PATH/locale environment, with no credential/proxy variables. Cwd is an owned empty0700 directory `<root>/<id>/model-work/<invocation.ID>.<invocation.Nonce>`, retained within that invocation's metadata allowance. Private socket path follows the existing bounded local socket/ownership discipline, using a distinct model prefix and exact invocation identity; both producer and consumer derive/compare the identical path. Neither path comes from model-generated content.

## Wire bounds and canonical form

- Request encoded maximum64KiB; prompt UTF-8 maximum32KiB. Reject, do not silently truncate. The request must also fit the existing complete64MiB host-envelope/metadata allowance. This is a typed model envelope, not a source archive: do not apply Source ArchiveBytes to its stdin or enlarge global source limits.
- Version is integer1. Integers have canonical JSON integer spelling, no fraction/exponent/negative zero. Hashes are lowercase64hex; existing ScopeHandle/ManagedRuntimeReference validators remain mandatory. Path/ID strings inherit existing bounds plus the limits here.
- Every listed wire field is required, including empty result text/error and empty arrays. No nulls, unknown fields, duplicate keys, case aliases, trailing JSON or invalid UTF-8. Empty slices encode `[]`, never null. Apply raw duplicate/alias checks before typed decode. These rules apply recursively to protocol structs; model-provided function arguments are equally strict.
- Times are nonzero UTC with exact `time.Time.UTC().Format(time.RFC3339Nano)` spelling and `Z`; no offset aliases, overprecision or zero time. Canonical encoding uses `encoding/json.Marshal` of the listed structs with no extra whitespace/newline.
- MaxTurns equals the admitted maximum and must be1..1024 for this adapter; a larger admission is unsupported before dispatch, not silently rewritten. InputTokenCeiling is exactly16384; OutputTokenCeiling exactly4096.
- TranscriptByteLimit equals `min(limits.HostOutputBytes, limits.TranscriptBytes, 1<<20)`. Both cumulative retained conversation bytes (including provider items/tool results) and the final encoded ModelResult must fit it. No extra unbudgeted transcript file or per-turn request/result artifacts. Bound reads before allocation; crossing a cap is interruption, never success with dropped evidence.
- ModelResult outcome is `completed`, `failed` or `interrupted`. Completed requires empty ErrorCode, a final provider response without pending calls and nonempty bounded FinalText (<=64KiB). Failure/interruption has a bounded constant machine ErrorCode (<=64 ASCII bytes), not provider body/secret-derived diagnostics. FinalText may be empty on failure. TurnsDispatched is0..MaxTurns and equals the number of records with ModelDispatched=true. There are0..MaxTurns records, each with a1-based contiguous Turn ordinal. Create a record before its count request; CountDispatched=true means counting may have occurred, and ModelDispatched=true requires a successfully validated count. A count-only failed/unknown attempt remains recorded and charged even though it does not increment TurnsDispatched. Each record's outcome is `completed`, `incomplete`, `failed` or `unknown`; IDs/model strings <=256bytes. Unknown dispatched response may have empty response/model IDs and zero unknown usage, but retains its full precharged liability. LiabilityCents includes both independently precharged count and model request liabilities for that record. ToolCallIDs is[] or one bounded ID for parallel_tool_calls=false. Sum of record liabilities equals LiabilitySpentCents and never exceeds request allowance. The request/result association includes exact invocation and canonical request hash.

## Exact money and turn accounting

Authoritative current source is `RuntimePolicy.MaxModelInvocationUSD string` and `managedInvocationCents` in `managed_contract.go`. It already accepts only positive decimal values with at most two fractional digits, with checked exact conversion. There is no managed float allowance to adapt. Reuse that parser unchanged; do not introduce float currency or fractional-cent compatibility.

`InvocationLiabilityCents` must equal the exact parsed admitted allowance. Reserve that entire amount conceptually with the counted M invocation before dispatch and never refund after any outcome. Examples: `"0.01"` gives1; `"1.2"` gives120; `"0.009"` is rejected by the existing policy, never rounded to1. Do not increase or reinterpret historical admission.

Before each token-count or model request, precharge its worst-case liability using a reviewed qualified tariff. All arithmetic is checked integer/rational arithmetic: round each request's computed liability UP to whole cents, but never round the admitted allowance up. For a positive exact rational request cost p/q dollars, cents=ceil(100*p/q). If checked cumulative liability would exceed the invocation allowance, send no request. No refund of over-reserved output, cached-input discount or unknown request liability. Rates and request fees are not supplied by the model or arbitrary caller.

No qualified tariff exists yet for this implementation baseline, especially for the token-count endpoint. Production transport MUST return adapter-unavailable before even counting tokens or reading credentials. Recording transport uses explicitly test-owned qualified fixtures through a private transport seam, never a public endpoint/price override or production recording flag. A future live qualification must establish all fees/modifiers and enforce an exact supported profile; absence from a pricing table does not establish zero cost.

Once qualified, call `/v1/responses/input_tokens` on the exact complete model/input/instructions/tools/history to be submitted; require `object == "response.input_tokens"`, nonnegative bounded integer input_tokens <=16384. This count request is within the same helper invocation and deadline and has its own precharged tariff liability. There is no model generation on count, but no assumption about fees. Changed payload requires a new counted/priced preflight; no automatic retries. The subsequent Responses call has max_output_tokens4096 including reasoning. Increment the in-memory dispatch ordinal and precharge before the HTTP effect. Error/timeout/lost response ends the invocation; never repeat it.

One helper lifetime consumes ONE M reservation/host-kind scope, irrespective of turns. Full allowance stays spent after crash; recovery never reconstructs/resumes internal counters to run that invocation again. Thus no M-times-turns scopes/events/files are introduced. T budgets every tool invocation, H every additional host invocation where applicable; never hide an extra wrapper/process or charge a new M for each HTTP turn. Existing cleanup C and SQLite/storage caps remain unchanged.

## Trusted recording construction

`RunTerraModel(ctx context.Context, request ModelRequest, output io.Writer) error` remains the actual registered helper consumer. Its private loop accepts a constructor-created private session containing the HTTP RoundTripper and qualified tariff; no exported injection API. The loop leaf owns `model_terra_transport.go` with build constraint `!oro_factory_recording` and `model_terra_recording.go` with `oro_factory_recording`. Both implement the same unexported `newTerraSession() (terraSession, error)` constructor. The private session has `transport http.RoundTripper` and `liability func(kind string, inputTokens, outputTokens int) (int64, error)`; kind is exactly `count` or `response`. The latter is the checked, upward-rounded qualified tariff computation already required above, never caller wire data. Normal construction returns unavailable until live qualification. The recording implementation provides a fixed in-memory RoundTripper and fixed test tariff, rejects all unexpected methods/origins/paths/payloads, and has no network fallback. There is no runtime endpoint, price, recording flag, environment override, or request-selected scenario. Unit tests can construct private sessions directly for adversarial responses. This compile-time fixture is trusted test code, not a supported production profile.

The CLI proof builds the actual executable with `go build -tags oro_factory_recording -o <fixture>/oro ./cmd/oro`, using the same race/coverage instrumentation where applicable. It admits that executable as ModelExecutable and publishes those identical bytes as frozen Oro; normal self-path/hash validation is retained. Actual registered `_model-terra` invokes the actual loop and compile-time constructor in its real process group. Use a test-created private credential containing a synthetic nonsecret token to exercise the normal credential reader; never open existing credentials. The fixed recording sequence is count -> strict container-tool call -> count with tool result -> final text; validate complete request bodies and liability at each step. Docker/launchctl recording remains at existing host transport seams. No fabricated grants or references, fake helper result, TestMain command substitution, or production endpoint override qualifies this proof. The CLI-proof task owns building/running this fixture; the loop task owns its two constructor files. Ordinary builds and tests must also prove unqualified transport returns unavailable with zero credential/network access.

## Composed architecture and repair inputs

Keep the existing 32KiB Prompt and 64KiB canonical ModelRequest caps. The later Astra plan is bounded to 8KiB **canonical encoded bytes**, at most32 steps, with exact schema `ArchitectPlan{Version int json:"version"; ObjectiveSHA256 string json:"objective_sha256"; BaseSHA string json:"base_sha"; Steps []string json:"steps"; Invariants []string json:"invariants"}`. Required fields, version1, canonical/null/alias rules match this document. This deliberately replaces the earlier 64KiB plan allowance. Required repair feedback is at most4KiB UTF-8, and its complete structured verdict/receipt representation must also pass the existing receipt decoder bounds. It is not conveyed through the engine's truncated diagnostic rejection string.

Role tasks own `composeModelPrompt` as a private deterministic composer: a version1 JSON object with fields in order `version` (integer1), `objective` (complete admitted objective), `context` (the required trusted ModelRoleContext below), `architecture` (complete canonical ArchitectPlan object, or absent only before architecture exists), and `feedback` (complete required feedback string, empty for initial engineering). No source archive is embedded. Context is present even for the initial Terra worker; source/check/receipt identities must never be inferred from free-text objective or scope IDs.

```go
type ModelSourceFile struct {
    Path string `json:"path"`
    SHA256 string `json:"sha256"`
    Content string `json:"content"`
}
type ModelProtectedAcceptance struct {
    CandidateSHA string `json:"candidate_sha"`
    Passed bool `json:"passed"`
    Checks []string `json:"checks"`
    Feedback string `json:"feedback"`
}
type ModelRoleContext struct {
    BaseSHA string `json:"base_sha"`
    SourceSHA string `json:"source_sha"`
    SeedSHA256 string `json:"seed_sha256"`
    PolicyHash string `json:"policy_hash"`
    RequiredChecks []string `json:"required_checks"`
    SourceFiles []ModelSourceFile `json:"source_files"`
    CandidateSHA string `json:"candidate_sha"`
    Acceptance *ModelProtectedAcceptance `json:"acceptance,omitempty"`
    AcceptanceOutputSHA256 string `json:"acceptance_output_sha256"`
}
```

These shared protocol types are prompt data, not new authority or verdict protocols. Every field except Acceptance is required; empty strings/arrays are permitted only under the role rules below. Explicit null is invalid, including Acceptance. Source Git IDs use existing `sourceOID`, not an invented SHA256-only Git format. Content hashes use lowercase64hex. Context encoded maximum12KiB, SourceFiles maximum16 entries in lexicographic path order, total UTF-8 Content maximum8KiB; each content is complete, never a truncated excerpt. Paths are normalized repository-relative source paths under the existing source-path validator, unique and nonescaping. SHA256 equals the complete content bytes. RequiredChecks is the exact committed Spec.RequiredChecks list; do not abbreviate it to fit. Oversize follows the same explicit failure route below.

For initial worker and architect, BaseSHA and SourceSHA both equal the current snapshot.BaseSHA and workspace.SourceSHA; SeedSHA256 equals the verified workspace.SeedSHA256; PolicyHash equals snapshot.PolicyHash. CandidateSHA and AcceptanceOutputSHA256 are empty and Acceptance is absent. Initial worker may have empty SourceFiles because it can inspect its seeded /source through the restricted tool; the architect task must provide at least one complete task-relevant source file from the verified base tree, selected by trusted controller policy, and never from mutable candidate output. Its required file selection is frozen for that architecture request; if required files cannot fit, fail oversized rather than drop them. Planning cannot claim a whole-repository inspection from this bounded packet.

Independent review and repair require nonempty CandidateSHA equal to the current snapshot.CandidateSHA after trusted candidate import, distinct from the base when applicable. SourceSHA equals that exact candidate commit; SeedSHA256 still identifies the original workspace seed (it is not presented as a candidate content hash). SourceFiles must include complete trusted candidate-version files for every file the review policy requires; policy-required changed-file coverage that exceeds these caps is unsupported, not silently sampled. BaseSHA, PolicyHash and RequiredChecks remain the original frozen values. Acceptance is mandatory and is a typed projection of the actual protected acceptance command stdout decoded by existing decodeReceipt, with the exact candidate/check association validated by receiptPrerequisites/receiptChecks and the protected execution result. AcceptanceOutputSHA256 hashes that original complete stdout, not this projection. Reuse acceptanceReceipt semantics and its trusted producer; model output cannot populate this field. Independent approving review requires actual passed acceptance evidence. Repair may carry a protected failed verdict only if its candidate/check/process identity is valid; invalid/missing protected evidence is an authority error, not fabricated failure feedback. Existing reviewReceipt continues to carry the independent review result. Do not treat feedback text or this context projection as replacement acceptance evidence.

The controller constructs context from verified journal/workspace/source and protected policy results and binds the complete prompt in the reserved canonical ModelRequest hash. The registered helper sends the complete prompt as provider input. Loop/consumer/CLI recording assertions must inspect the actual count and Responses request bodies and verify exact base/source/seed/policy/check values; role-task recordings additionally verify candidate SHA, complete selected source content and protected receipt fields/output hash. Tests with an objective deliberately lacking all identities must still carry this context. Mismatched source/candidate/check/receipt or missing required context denies before the next M reservation/provider effect. Architecture acceptance must preflight the exact future Terra prompt including context; repair acceptance must preflight the exact next role prompt including protected receipt and feedback. Validate the full resulting UTF-8 prompt <=32KiB and the full encoded ModelRequest <=64KiB, including JSON escaping and all identity fields, before accepting a plan for engineering or reserving any next model invocation. The existing input ceiling16384 still requires the qualified count preflight; byte admission is not a token-count guarantee. Full current provider history must pass the same cumulative transcript/token limits.

An oversized required plan, feedback, composed prompt or encoded request is the explicit non-retryable `model_input_too_large` stage outcome: retain already-spent invocation/attempt, reserve no next M/tool/host work, retire any live candidate through the existing path, and stop/escalate the attempt. Never silently shorten objective, architecture, invariants or review feedback. Emit only that constant diagnostic into the engine rejection mechanism (existing16KiB reason limit), not the full plan/feedback. Tests include individually valid8KiB plan plus a large objective exceeding32KiB after composition, escaping that breaches64KiB request,4KiB feedback boundary, and inputs at exact limits. Oversize must not become an automatic retry with the same impossible input. Independent acceptance/review receipts remain the sole verdict protocol.

## Restricted tool channel

One connection, one in-flight request, monotonically increasing sequence; no reconnection/replay. The controller serves it concurrently while HostRuntime.Invoke waits for the model helper. Kernel UID/PID/PGID authenticate the actual helper as a member of the exact granted wrapper group, not a JSON PID claim. The helper inherits that wrapper group; controller PID equality is not the helper identity rule. Revalidate current invocation/candidate authority before every effect.

```go
type ModelToolHello struct {
    Version int `json:"version"`
    Type string `json:"type"` // HELLO
    Invocation ScopeHandle `json:"invocation"`
    RequestSHA256 string `json:"request_sha256"`
    CandidateID string `json:"candidate_id"`
    CandidateBindingSHA256 string `json:"candidate_binding_sha256"`
}
type ModelToolCall struct {
    Version int `json:"version"`
    Type string `json:"type"` // CALL
    Sequence uint64 `json:"sequence"`
    Command string `json:"command"`
    TimeoutSeconds int `json:"timeout_seconds"`
}
type ModelToolReply struct {
    Version int `json:"version"`
    Type string `json:"type"` // RESULT
    Sequence uint64 `json:"sequence"`
    Stdout []byte `json:"stdout"`
    Stderr []byte `json:"stderr"`
    ExitCode int `json:"exit_code"`
    Uncertain bool `json:"uncertain"`
    ErrorCode string `json:"error_code"`
}
```

Frames use4-byte big-endian unsigned encoded-length prefix, then exactly that many canonical JSON bytes, maximum64KiB. HELLO is followed by the first CALL on the same authenticated connection; no second result-publication protocol or model-specific GO is introduced. The actual host wrapper's existing HELLO/GO/PUBLISHED/retirement protocol remains authoritative. A rejected HELLO closes the connection; it never accepts a CALL. Calls begin sequence1. Command is1..8192 UTF-8 bytes without NUL; timeout_seconds is integer1..3600 and must not exceed remaining original candidate time. Stdout/Stderr are canonical base64 JSON byte strings (empty means "", not null), combined decoded bytes <=32768 and existing host-output cap; full frame still <=64KiB. ErrorCode is empty for a certain completed exec; uncertain replies use constant bounded code and permanently poison the channel. Nonzero command exit can be a certain tool result; it is not a receipt.

The model sees only function `factory_container_exec` with strict object arguments exactly `{command:string, timeout_seconds:integer}`. Responses uses parallel_tool_calls=false. Controller derives endpoint/container/UID1000 and original deadline from trusted candidate state. Tool cwd is exactly `/source`, the actual seeded root. Execute exactly `/bin/sh -c <command>` inside that container, without login/profile flags. Set the explicit tool environment `PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin`, `HOME=/scratch`, `TMPDIR=/scratch`, `LC_ALL=C`, `LANG=C`; no caller additions or inherited host environment. Existing inspected image environment must not introduce additional executable search locations or credentials. No shell interpretation occurs on the host. Timeout/unknown exec prevents subsequent turns/tools and requires retirement/reconciliation; successful foreground command completion is not proof that detached descendants exited. Close/drain listener and positively retire helper before candidate retirement. Never export from a live or uncertain candidate.

The current pinned Go1.26.10 linux/arm64 base plus guard has evidence for `/bin/sh`, UID1000 and basic id/stat/cmp operations. It is not a qualified full-Oro offline checker image. Full qualification still needs the existing Go/CGO/module/tool, Node/Python lock, frozen vulnerability database, Git and staged-asset inventory, with bounded hashed offline payload provenance and measured retention. This contract neither invents a payload quota nor expands the two-file guard builder into an image framework.

## Responses and result validation

Fixed production HTTPS origin `https://api.openai.com`, exact endpoint paths, certificate validation, no redirects or environment-derived proxy, bounded request/response bodies and context deadlines. Use no hosted tools, discovery, web, native shell, patch, connector or model-selected URL. Stateless bounded history includes required provider reasoning/output items; do not substitute previous_response_id/server history which escapes local input accounting. Provider HTTP objects are bounded and duplicate-checked; only explicitly supported output item types can affect dispatch. Function arguments always pass the strict internal schema. Unknown tool/output action, incomplete response, changed observed model, malformed usage or oversized item fails closed.

Result.stdout is the one canonical ModelResult consumed only after normal HostRuntime quiescence. It is not protected acceptance, proof of source state or permission to promote. An observed model string does not establish immutable weights; record provider response IDs/models and reject identity drift, while retaining that limitation for live qualification.

## Counted invocation schema and producer boundary

The intent leaf owns the narrow admission extension in
`pkg/factory/managed_admission.go`, in addition to its listed model/store/history
and registered helper sources. Add `ModelIntentSchemaVersion int` with JSON tag
`model_intent_schema_version,omitempty` to both `managedManifest` and
`managedJournalAdmission`; `managedJournalIdentity` copies it exactly. Only values
0 and 1 are valid. Absent legacy zero preserves canonical bytes. Explicit null,
aliases, duplicates and noncanonical representations fail existing canonical
validation. Only fresh `newManagedManifest` construction sets version1; replay
never upgrades an existing marker or backfills a table.

Version0 admissions remain metadata-readable but model-unavailable, even if a
stray model table exists. Version1 requires the model-intent table during
activation, history and metadata reads, including before the first M invocation;
a missing table is corruption. The frozen manifest/journal identity and existing
activation admission digest bind this capability. Reads and reservations never
create or migrate schema. Present version1 tables require bounded canonical
row/event/M-token/scope association and revision integrity; pristine activation
requires no model intents. Deleting a table, row or event must never restore work
or renew authority.

The reservation producer accepts a request template with zero Invocation and
empty ToolSocket, generates its own invocation scope, fills the exact scope and
socket before canonical encoding/hash, and rejects supplied invocation authority.
The model token, host-kind scope, full admitted cents liability and intent/event
commit atomically; the invocation itself does not consume an extra H token.
Later actual tool/host effects retain their separate budgets. The helper's
post-GO verifier authenticates its actual inherited process group against the
granted wrapper group and revalidates parent controller identity without
requiring the helper PID to equal the controller PID. Unknown dispatch is never
replayed, refunded or given a renewed deadline.

Socket derivation is shared privately by producer, verifier and tool consumer.
Hash the canonical `json.Marshal` bytes of this exact ordered struct, using the
existing complete ScopeHandle encoding:

```go
struct {
    StateRoot string `json:"state_root"`
    ObjectiveID string `json:"objective_id"`
    Invocation ScopeHandle `json:"invocation"`
}
```

Use lowercase SHA256 hex, first32 characters. The path is
`/private/tmp/oro-model-<digest>/s` on Darwin and `/tmp/oro-model-<digest>/s` on
Linux; other platforms are unsupported. Never consult TMPDIR or another
environment-derived base. The tool consumer exclusively creates the owned0700
directory and socket0600 only after durable reservation; existing paths fail
closed without adoption. Intent reservation only derives and binds the path,
without creating directories or listeners.

Intent tests use genuine internal admission/candidate/reservation/process-group
producers and label synthetic transport fixtures honestly. Existing entry tests
must now expect authority denial for their shape-only requests, while preserving
executable-identity checks. The full public publication through counted producer
and registered helper positive belongs to the actual CLI consumer/proof leaves;
never export a private producer or insert a fabricated grant to simulate it.

## Task graph and verification boundary

`oro-mf-terra-entry` -> `oro-mf-terra-intent` -> (`oro-mf-terra-loop`, `oro-mf-terra-tools`) -> `oro-mf-terra-consumer` -> `oro-mf-terra-cli-proof` -> `oro-mf-astra-plan` -> `oro-mf-model-repair` (also requires `oro-mf-acceptance`). Existing milestone IDs `oro-mf-claude`/`oro-mf-mcp` are retained for tracker continuity, not provider selection.

Design gate `oro-mf-terra-spec-proof` blocks entry until an independent Astra PASS artifact names the exact frozen document SHA and Markdownlint passes; later document changes invalidate that proof. Every task reads this document before its own listed sources. Entry has root-approved mechanical source-scope exception to keep public start/publication/reader/registered consumer atomic. Authority uses genuine admitted candidate and process-group fixtures. The first combined recording proof must run actual public start/hidden commands/Supervise/RunManagedController/helper/tool dispatch/retirement, preserving budgets/attempts/deadlines and no secret/native effects. Until export/import exists, return explicit candidate-export-unavailable after successful model/retirement, never fake delivery success.

Unknown model/exec outcomes and physical-stopped/journal-retiring final-COMMIT reconciliation remain owned by existing supervisor/docker-scope recovery milestones. Ready-only retirement is not automatic recovery. Exact-SHA protected acceptance, independent read-only Astra review, bounded feedback/escalation and a real authenticated model trial remain full-goal requirements.

## Official evidence and unresolved qualification

- [Counting tokens](https://developers.openai.com/api/docs/guides/token-counting): exact input-token endpoint and response shape; does not establish a zero endpoint fee.
- [Function calling](https://developers.openai.com/api/docs/guides/function-calling): application-side execution, strict function parameters and parallel_tool_calls=false.
- [Reasoning limits](https://developers.openai.com/api/docs/guides/reasoning): max_output_tokens includes reasoning, and incomplete results can cost tokens without visible output.
- [Terra model](https://developers.openai.com/api/docs/models/gpt-6.1-terra): Responses/tool support and published pricing modifiers; alias is not a dated immutable snapshot.

No model/API/native/credential calls were used to produce this contract. Live transport stays unavailable until root accepts explicit tariff/count-fee/provisioning/tool-boundary qualification. Never substitute a guessed tariff to make a recording test green.
