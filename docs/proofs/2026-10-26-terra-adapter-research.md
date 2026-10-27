# Restricted Terra engineer adapter research

Date: 2026-10-26. Astra inspected local code/CLI help and fetched official OpenAI
documentation. No credentials were inspected, model requests sent or native
runtimes launched. This is research, not an implemented or qualified adapter.

## Existing launcher

Installed Codex CLI reported 0.164.0. Its inspected help does not establish
exhaustive native-tool exclusion or hard turn/dollar caps. No qualifying CLI
configuration was verified; this does not prove every possible configuration
incapable. Oro's `pkg/agentruntime/codex/codex.go` inherits environment, selects
danger-full-access and bypasses hook-trust prompts. Do not reuse that launcher
as the managed boundary. Feature disabling in `pkg/dispatcher/estimate.go` is
not exhaustive capability proof; do not copy its credential handling.

## Narrow alternative

Official documentation lists gpt-6.1-terra and function calling. An Oro-owned
Responses loop can offer exactly one strict factory_container_exec function;
application code decides whether to execute returned arguments. Supply no hosted
shell, patch, browser, search, MCP discovery or other built-in execution tools.
Disable parallel tool calls and validate the sole allowed name/schema. Sources:
[Terra model](https://developers.openai.com/api/docs/models/gpt-6.1-terra),
[function calling](https://developers.openai.com/api/docs/guides/function-calling).

Bind execution to the exact ready candidate. Derive container, endpoint,
UID/GID1000, cwd, environment, resource/byte limits and original worker deadline
from durable authority; model arguments cannot choose them. Each tool effect
uses the existing counted HostRuntime path. Prose is only engineer completion;
protected acceptance independently decides delivery.

Reserve full model invocation liability before dispatch and count requests,
turns and tool effects durably. Preserve existing MaxModelCalls/MaxModelTurns
semantics; do not silently redefine them. Unknown HTTP completion remains
charged, without blind retry. Bound request/response/transcript/tool-output bytes
independently of tokens. Maximum output tokens includes reasoning and visible
output, but does not itself cap dollars. A reviewed input-token upper bound,
conservative price arithmetic and per-request liability reservation remain
required. Source:
[reasoning controls](https://developers.openai.com/api/docs/guides/reasoning).

## Remaining evidence

Credential provisioning/API access, pinned executable packaging, durable turn
accounting and conservative cost calculations need concrete contracts. Secrets
must not enter model-visible input or transcripts. The fetched model page did
not establish a dated immutable snapshot: pinning gpt-6.1-terra alone does not
prove frozen server weights. Record observed response identities and this limit.
Preserve Terra engineer/Astra architect roles; do not substitute older defaults
or historical Claude flags.

First use recording HTTP with the real candidate dispatcher: unknown/malformed/
duplicate tools, model drift, limits, overflow, revocation, expiry and uncertain
completion must produce zero unauthorized effects. A positive fixture must reach
response, counted restricted exec, bounded result and next response. Native and
real-model qualification follow separately; fixtures do not prove autonomous
engineering or delivery.

## Implementation boundary selected after repository audit

This section is a design decision, not implemented model execution. A frozen,
Oro-owned `oro-model-terra` helper should occupy the existing ModelExecutable
slot and run through authenticated HostRuntime under one model reservation and
one fixed invocation deadline. Its bounded Responses turns remain inside that
single, non-resumable helper lifetime. Existing retention counts model invocations,
not a new model scope for every HTTP turn. Reserve the full conservative invocation
liability once; internal request allocations cannot exceed it. Unknown helper or
HTTP outcome burns that reservation and prohibits relaunch. The earlier per-request
reservation proposal above needs this invocation-level accounting interpretation;
it is not permission to silently multiply retained scopes or change admission.

The trusted helper owns fixed-endpoint HTTP, turn/input/output/transcript limits
and conservative price arithmetic. It evaluates no native commands. Only the
single restricted container-exec function may produce effects, through a live
controller listener authenticated to the helper's actual granted PID and scope.
Tool and host effects retain separate durable reservations. The listener must
exist while HostRuntime.Invoke blocks and close/drain before final handling.

Credentials require explicit provisioning. The proposed narrow contract binds a
nonsecret absolute credential reference into the frozen runtime. The helper reads
it privately after authenticated GO; no credential value may enter persisted
invocation Env, stdin, argv, runtime JSON, model input, container, transcript or
logs. A private regular-file reader needs ownership/mode/link/path checks and
bounded reads. Do not import existing Codex authentication. This contract and its
packaging remain to be implemented and reviewed; no additional API broker or
Keychain service is required for the trusted helper design.

A Docker CLI exit does not prove container-side exec descendants are gone. The
candidate container is the containment/retirement unit. Successful bounded tool
results may feed later turns in the same still-live candidate; timeout or unknown
exec outcome poisons it and prohibits further turns or tool dispatch. Exact
candidate stop and positive stopped inspection are prerequisites for accepting
model completion or exporting source. A missing container is not export evidence.
The guard remains an unprivileged fixed-deadline PID1, with no model control
channel. The ready-only retirement precursor is tracked as
`oro-mf-candidate-retire`; it does not yet qualify unknown-exec recovery.

The proposed helper entry is `factory _model-terra` in a build explicitly selected
as ModelExecutable. Freeze those configured bytes in the existing model slot;
never replace them silently with the controller executable. A versioned helper
protocol and nonsecret credential reference need runtime publication/digest
binding. Historical metadata stays readable; absent adapter configuration means
execution unavailable, without reconstructing artifacts.

The recording design starts with 16,384 input and 4,096 output token ceilings,
strict single-function calls, no redirects/proxy inheritance and complete bounded
responses. The official [token-counting endpoint](https://developers.openai.com/api/docs/guides/token-counting)
can count the complete Responses input before dispatch. The inspected material
has not qualified that endpoint's fee treatment or every pricing modifier. Live
transport must not claim a hard dollar cap until those costs are qualified; do
not invent a free endpoint assumption. These are unresolved live-qualification
requirements, not implemented API requests or permission to change admission.
