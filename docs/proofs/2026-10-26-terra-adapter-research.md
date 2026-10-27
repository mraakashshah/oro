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
