# Enterprise Readiness Design — Claude Code Pattern Adaptation

**Date:** 2026-03-31
**Status:** Reviewed — R1 FAIL (3 critical gaps), R2 PASS (3 minor gaps noted). Ready for beadcraft decomposition.

## Goal

Adapt 5 proven enterprise patterns from Anthropic's Claude Code CLI into oro's existing architecture to make the swarm visible, auditable, scalable, and extensible — transforming oro from a developer tool into an enterprise platform.

## Context: Why Now

Anthropic open-sourced Claude Code (512K LOC TypeScript) on March 31, 2026. Their production codebase reveals enterprise-grade patterns that oro lacks — not because the architecture is wrong, but because the exposure layer is missing. Oro's swarm works; outsiders just can't see it, audit it, deploy it, or extend it.

**Oro's moat (keep):** Process-enforced quality (19-check QG, ops review, TDD enforcement), isolated concurrent execution (worktrees + UDS), context exhaustion handling (handoff loops), cross-session memory (SQLite FTS5).

**Oro's gap (fix):** No metrics endpoint, no audit trail, no container story, no extensibility protocol, no IDE integration.

## Pattern → Integration Mapping

### Pattern 1: Structured Observability (Prometheus + Grafana)

**Source:** Claude Code's `grafana/dashboards/` (4 pre-built dashboards), `src/cost-tracker.ts` (per-model USD attribution), `src/services/analytics/` (typed event sink).

**Integration point:** Oro's dispatcher already emits 123 `logEvent()` calls across the codebase (`dispatcher.go`). Each call writes to the SQLite `events` table with type/source/bead_id/worker_id/payload. The data exists — it just needs a Prometheus exporter.

**What changes:**

1. **New package `pkg/metrics/`** — Prometheus registry with counters, gauges, histograms. Wraps `prometheus/client_golang`.

2. **HTTP metrics server in Run()** — Start an `http.ServeMux` as a goroutine in `dispatcher.go:Run()` between UDS listener start and `acceptLoop`. Register `/metrics` (Prometheus), `/healthz`, `/readyz`. Shut down gracefully in `shutdownWithTimeout()` before UDS close. **If bind fails and MetricsEnabled=true, Run() returns error (fatal). If MetricsEnabled=false, skip silently.**

3. **Refactor logEvent() to support MetricsObserver** — `logEvent()` (`dispatcher.go:3617`) is a bare SQL INSERT called 123+ times. Add a `MetricsObserver` interface field to Dispatcher:
   ```go
   type MetricsObserver interface {
       Observe(eventType, source, beadID, workerID, payload string)
   }
   ```
   `logEvent()` calls `d.metricsObserver.Observe()` after the SQL INSERT (one-line change). The `pkg/metrics` package implements this interface, mapping event types to Prometheus metrics. When MetricsEnabled=false, use a no-op observer.

4. **Instrument existing event path** — The MetricsObserver maps event types to Prometheus metrics. No individual call sites modified — the observer is called once in logEvent().

   | Event Type | Metric | Kind |
   |-----------|--------|------|
   | `heartbeat` | `oro_worker_context_pct{worker_id}` | Gauge |
   | `done` | `oro_beads_completed_total{verdict}` | Counter |
   | `merged` | `oro_merges_total{target_branch}` | Counter |
   | `quality_gate_rejected` | `oro_qg_rejections_total` | Counter |
   | `merge_conflict` | `oro_merge_conflicts_total` | Counter |
   | `epic_acceptance_passed` / `_failed` | `oro_epic_acceptance_total{result}` | Counter |
   | `memory_consolidation` | `oro_memory_consolidations_total` | Counter |
   | Any handoff-related | `oro_handoffs_total{reason}` | Counter |
   | Assignment timing | `oro_bead_duration_seconds{phase}` | Histogram |
   | Worker pool size | `oro_workers_active` | Gauge |
   | Queue depth | `oro_beads_queued` | Gauge |

5. **Cost data pipeline (worker → dispatcher)** — This is a critical wiring gap. Worker's `streamjson.go` already parses `CostUSD` from claude CLI output (`Activity.CostUSD`), but `DonePayload` in `protocol/message.go` has NO cost fields. Cost data dies in the worker process. Fix:
   - Add `CostUSD float64`, `InputTokens int`, `OutputTokens int` to `protocol.DonePayload` (`protocol/message.go`)
   - Worker accumulates costs across all `Activity` results during bead execution
   - Worker's `SendDone()` (`worker.go`) populates cost fields in DonePayload
   - Dispatcher's `handleDone()` (`dispatcher.go`) consumes cost fields, updates per-bead cost accumulator, emits `oro_api_cost_usd{model}` counter and `oro_api_tokens_total{model,direction}` counter via MetricsObserver

6. **Grafana dashboards** — Ship 3 JSON provisioning files in `assets/grafana/`:
   - `swarm-overview.json` — Workers active, beads/hour, QG pass rate, queue depth
   - `cost-attribution.json` — USD/hour by model, by epic, by worker
   - `worker-health.json` — Context % over time, handoff frequency, idle time

**Config additions to `Config` struct:**
```go
MetricsAddr string // HTTP address for /metrics endpoint (default ":9090")
MetricsEnabled bool  // Enable Prometheus metrics (default false)
```

**Files modified:**
- `pkg/dispatcher/dispatcher.go` — Add MetricsObserver field to Dispatcher struct, add metricsObserver.Observe() call in logEvent() (~line 3617), start HTTP server in Run() (~line 703), shut down HTTP server in shutdownWithTimeout() (~line 779), add MetricsAddr/MetricsEnabled to Config, wire New() (~line 506)
- `pkg/dispatcher/health.go` — Add /healthz and /readyz HTTP handlers
- `pkg/protocol/message.go` — Add CostUSD, InputTokens, OutputTokens to DonePayload (~line 148)
- `pkg/worker/worker.go` — Accumulate costs, populate cost fields in SendDone() (~line 1088)
- `pkg/worker/streamjson.go` — Extract token usage from streaming JSON (extend existing Activity parsing)
- `cmd/oro/cmd_start.go` — Add --metrics-addr flag, pass to buildDispatcher() (~line 585)
- `cmd/oro/root.go` — (no change needed for P1, but note for P2/P4 command registration)
- `go.mod` — Add `github.com/prometheus/client_golang`
- `Makefile` — Add `assets/grafana/` to stage-assets target

**Files created:**
- `pkg/metrics/metrics.go` — MetricsObserver implementation, Prometheus registry, metric definitions
- `pkg/metrics/metrics_test.go`
- `pkg/metrics/noop.go` — No-op MetricsObserver for when metrics are disabled
- `assets/grafana/swarm-overview.json`
- `assets/grafana/cost-attribution.json`
- `assets/grafana/worker-health.json`

**What we're NOT doing:**
- No OpenTelemetry tracing. Overkill for a single-machine swarm. Prometheus counters are sufficient.
- No Datadog/GrowthBook vendor integration. Open standards only.
- No real-time streaming metrics. Pull-based Prometheus scraping is the right model.

---

### Pattern 2: Enterprise Audit & Permission Model

**Source:** Claude Code's `src/hooks/toolPermission/` (7-level permission scoping), `src/services/remoteManagedSettings/` (enterprise admin config), `src/services/policyLimits/` (quota enforcement).

**Integration point:** Oro's dispatcher is a natural audit chokepoint — every bead assignment, review verdict, merge, and escalation flows through `dispatcher.go`. The `events` table already captures lifecycle events but lacks audit-grade structure (no actor, no decision, no rule reference).

**What changes:**

1. **Audit table** — New SQLite table in `SchemaDDL` (protocol/schema.go):
   ```sql
   CREATE TABLE IF NOT EXISTS audit_log (
       id INTEGER PRIMARY KEY,
       timestamp TEXT NOT NULL DEFAULT (datetime('now')),
       actor TEXT NOT NULL,       -- 'dispatcher', 'worker:<id>', 'ops:<type>', 'user'
       action TEXT NOT NULL,      -- 'assign_bead', 'approve_review', 'reject_review', 'merge', 'escalate', 'scale', 'shutdown'
       target TEXT NOT NULL,      -- bead_id, worker_id, epic_id
       decision TEXT,             -- 'allowed', 'denied', 'auto'
       rule TEXT,                 -- which policy rule applied (for future policy engine)
       metadata TEXT,             -- JSON payload (model used, cost incurred, etc)
       session_id TEXT            -- links to dispatcher session for correlation
   );
   CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_log(action);
   CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor);
   CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_log(target);
   ```

2. **Audit emission** — New `d.audit()` method called at decision points (not at every event — only at points where a decision was made):
   - Bead assignment → actor=dispatcher, action=assign_bead, target=bead_id, metadata={model, worker_id, priority}
   - Review verdict → actor=ops:review, action=approve/reject_review, target=bead_id
   - Merge → actor=dispatcher, action=merge, target=bead_id, metadata={commit_sha, branch}
   - Scale → actor=user/dispatcher, action=scale, target=worker_count
   - Escalation → actor=dispatcher, action=escalate, target=bead_id, metadata={type, reason}
   - Shutdown → actor=user, action=shutdown

3. **Cost limits** — New `Config` fields:
   ```go
   MaxCostPerBead    float64 // USD ceiling per bead (0 = unlimited)
   MaxCostPerEpic    float64 // USD ceiling per epic (0 = unlimited)
   MaxCostPerSession float64 // USD ceiling per dispatcher session (0 = unlimited)
   ```
   Checked in `assignBead()` before assignment. If limit exceeded → escalate to manager instead of assigning. Requires Pattern 1's cost tracking to be in place.

4. **Parameterized worker settings** — Extract hardcoded MCP allow-list from `cmd_init.go:991-996` into `Config` struct:
   ```go
   AllowedMCPTools []string // MCP tools workers can invoke (default: context7 only)
   AllowedModels   []string // Models workers can use (default: all)
   ```

5. **`oro audit` CLI command** — Query audit_log with filters:
   ```
   oro audit --action merge --since 24h
   oro audit --actor worker:w1 --target oro-abc1
   oro audit --format json  # for external tools
   ```

**Files modified:**
- `pkg/protocol/schema.go` — Add audit_log table to SchemaDDL (CREATE TABLE IF NOT EXISTS = idempotent) + new `MigrateAuditLog` constant for existing databases (consistent with MigrateFileTracking, MigrateKVStore pattern)
- `pkg/dispatcher/dispatcher.go` — Add audit() method, call at decision points (assignBead, handleReviewResult, handleMerge, handleEscalation, handleScale, handleShutdown), add MaxCostPerBead/MaxCostPerEpic/MaxCostPerSession to Config, add per-bead cost accumulator map, check cost limits in assignBead()
- `pkg/dispatcher/dispatcher.go:handleDone()` (~line 1019) — Consume cost fields from DonePayload, update per-bead cost accumulator
- `cmd/oro/cmd_init.go` — Parameterize MCP allow-list from Config (replace hardcoded lines 991-996)
- `cmd/oro/cmd_start.go` — Add --max-cost-per-bead, --max-cost-per-epic flags, pass to buildDispatcher() (~line 585)
- `cmd/oro/root.go` — Register newAuditCmd() via AddCommand (~line 24)

**Files created:**
- `cmd/oro/cmd_audit.go` — CLI for querying audit log with --action, --actor, --target, --since, --format filters
- `cmd/oro/cmd_audit_test.go`

**What we're NOT doing:**
- No remote managed settings (MDM). Oro doesn't have an admin server.
- No 7-level permission scoping. Oro's dispatcher is the sole authority — it doesn't need user/project/org layering.
- No wildcard permission rules for worker tools. Workers run in worktrees with claude's own permission system. Oro controls what beads they get, not what tools they invoke.

---

### Pattern 3: Containerized Deployment (Docker + Helm)

**Source:** Claude Code's `Dockerfile` (multi-stage build), `helm/claude-code/` (Kubernetes charts), `grafana/dashboards/infrastructure.json`.

**Integration point:** Oro builds via `Makefile` with `go:embed` for assets. The binary is self-contained. Containerization wraps this existing model.

**What changes:**

1. **Dockerfile** — Multi-stage:
   ```dockerfile
   # Stage 1: Build
   FROM golang:1.23-alpine AS builder
   RUN apk add --no-cache git make npm
   WORKDIR /src
   COPY . .
   RUN make build

   # Stage 2: Runtime
   FROM alpine:3.19
   RUN apk add --no-cache git ripgrep bash tmux
   COPY --from=builder /src/bin/oro /usr/local/bin/oro
   ENTRYPOINT ["oro"]
   ```

2. **Helm chart** — `helm/oro/`:
   - **Dispatcher** as StatefulSet (single replica, persistent volume for SQLite)
   - **Workers** as Deployment with configurable replicas
   - **Values:** `maxWorkers`, `models`, `costLimits`, `metricsEnabled`, `image.tag`
   - **ServiceMonitor** for Prometheus Operator (if Pattern 1's metrics are enabled)

3. **Health endpoints** — Added via Pattern 1's HTTP server:
   - `GET /healthz` → 200 if dispatcher goroutine is alive
   - `GET /readyz` → 200 if state != StateInert (ready to accept work)

4. **TCP transport option** — For cross-pod worker communication:
   ```go
   TransportMode string // "unix" (default) or "tcp"
   TCPAddr       string // TCP listen address when TransportMode="tcp" (default "127.0.0.1:9091")
   ```
   The UDS listener (`dispatcher.go:736`) already uses `net.Listener` interface. TCP is a `net.Listen("tcp", addr)` swap. Worker connection logic in `pkg/worker/` similarly uses `net.Conn`.

   **Critical wiring:** CLI tooling must also respect TransportMode:
   - `cmd_start.go:sendStartDirective()` hardcodes `net.Dial("unix", sockPath)` — must check TransportMode and dial TCP when appropriate
   - `cmd_start.go:pollForSocket()` also dials unix — same fix
   - `cmd_start.go:runDaemonOnly()` must advertise TCP address (not UDS path) when in TCP mode
   - Transport mode stored in a small config file (e.g., `~/.oro/projects/<name>/transport.json`) so CLI commands know which transport to use

   **Security:** TCP default binds to `127.0.0.1` only. Non-localhost requires explicit `--tcp-addr 0.0.0.0:9091`. No authentication in v1 (acceptable for same-machine, documented risk for network exposure). Auth deferred to post-v1.

5. **Asset mounting** — New runtime asset loader that checks ConfigMap mount path before falling back to `go:embed`:
   ```go
   func LoadAssets(mountPath string) (fs.FS, error) {
       if mountPath != "" {
           if info, err := os.Stat(mountPath); err == nil && info.IsDir() {
               return os.DirFS(mountPath), nil
           }
       }
       return EmbeddedAssets, nil  // fallback to go:embed
   }
   ```
   This enables skills hot-reload via ConfigMap updates without binary rebuilds.

**Files modified:**
- `pkg/dispatcher/dispatcher.go` — Add TransportMode/TCPAddr to Config, conditional `net.Listen("unix"|"tcp")` in Run() (~line 736)
- `cmd/oro/cmd_start.go` — Add --transport, --tcp-addr flags. Update `sendStartDirective()` and `pollForSocket()` to respect TransportMode (currently hardcode `net.Dial("unix", sockPath)`). Update `runDaemonOnly()` to write transport config.
- `cmd/oro/embed.go` — Add LoadAssets() with ConfigMap fallback
- `Makefile` — Add `docker-build`, `docker-push` targets

**Files created:**
- `Dockerfile`
- `docker-compose.yml` (local dev: dispatcher + 2 workers + Prometheus + Grafana)
- `helm/oro/Chart.yaml`, `values.yaml`, `templates/` (Helm chart must default to `transport: tcp` and have explicit dependency note on TCP transport)

**What we're NOT doing:**

**What we're NOT doing:**
- No Kubernetes operator. Helm is sufficient for v1.
- No distributed SQLite (Turso/LiteFS). Single-writer dispatcher means single SQLite file is correct.
- No worker auto-scaling based on metrics (HPA). Manual `oro scale N` via Helm values for v1. HPA as follow-up.

---

### Pattern 4: MCP Server (Extensibility Protocol)

**Source:** Claude Code's `src/services/mcp/` (client+server), `src/entrypoints/mcp.ts` (server mode), `mcp-server/` (standalone explorer).

**Integration point:** Oro already consumes MCP as a client (workers use `mcp__context7__` tools). The dispatcher's interfaces (`BeadSource`, `WorktreeManager`, `Escalator`, `ProcessManager`, `CodeIndex`) are clean integration seams that map to MCP tools.

**What changes:**

1. **New package `pkg/mcp/`** — MCP server using `github.com/mark3labs/mcp-go` (mature Go MCP library).

2. **MCP Resources (read-only state):**
   | URI | Description | Source |
   |-----|-------------|--------|
   | `oro://swarm/status` | Full swarm health (JSON) | `applyHealth()` |
   | `oro://beads/{id}` | Bead detail with AC, labels, priority | `BeadSource.Show()` |
   | `oro://beads/ready` | Ready queue | `BeadSource.Ready()` |
   | `oro://workers/{id}/logs` | Worker output log tail | File read from `~/.oro/workers/{id}/output.log` |
   | `oro://memories/search?q={query}` | Memory search results | `memory.Search()` |
   | `oro://audit?action={action}&since={duration}` | Audit log query | Pattern 2's audit_log table |

3. **MCP Tools (actions):**
   | Tool | Description | Maps to |
   |------|-------------|---------|
   | `submit_bead` | Create a new bead | `BeadSource.Create()` |
   | `close_bead` | Close a completed bead | `BeadSource.Close()` |
   | `focus_epic` | Set dispatcher focus to an epic | Directive: `focus <epic_id>` |
   | `pause_swarm` | Pause new assignments | Directive: `pause` |
   | `resume_swarm` | Resume assignments | Directive: `start` |
   | `scale_workers` | Set target worker count | Directive: `scale <n>` |
   | `query_memory` | Search project memory | `memory.Search()` |
   | `store_memory` | Add a memory entry | `memory.Store()` |

4. **MCP Notifications (event stream):**
   | Notification | Trigger |
   |-------------|---------|
   | `bead/completed` | Worker sends DONE + merge succeeds |
   | `bead/failed` | Max retries exhausted |
   | `worker/escalated` | STUCK_WORKER or max rejections |
   | `review/verdict` | Ops review completes |
   | `epic/completed` | All children closed + AC passed |

5. **Transport:** Stdio (for local `claude mcp add oro`) and HTTP (for remote/IDE consumption). HTTP piggybacks on Pattern 1's metrics server.

6. **Two entry paths (both required):**
   - **Standalone:** `oro mcp start` (stdio) / `oro mcp start --http` (HTTP). For `claude mcp add oro` usage. Reads dispatcher state from SQLite DB directly (read-only).
   - **Embedded:** `oro start --mcp` flag. MCP server runs as goroutine inside dispatcher's `Run()`, sharing the HTTP listener from Pattern 1. Has direct access to dispatcher state and can emit notifications on events. This is the primary path for IDE consumption.

7. **Dependency injection for MCP server** — The MCP server needs access to dispatcher internals (applyHealth, BeadSource, memory.Store, audit_log, directive channel). Define a `DispatcherAPI` interface in `pkg/mcp/`:
   ```go
   type DispatcherAPI interface {
       Health() (SwarmHealth, error)
       BeadSource() BeadSource          // reuse existing interface
       SearchMemory(query string) ([]memory.Entry, error)
       QueryAudit(filter AuditFilter) ([]AuditEntry, error)
       SendDirective(directive string) error
   }
   ```
   Dispatcher implements this interface. Standalone mode uses a read-only SQLite adapter. Embedded mode uses the live Dispatcher instance.

**Files modified:**
- `pkg/dispatcher/dispatcher.go` — Implement DispatcherAPI interface, add MCP server field, start MCP in Run() when --mcp, add event notification hooks in logEvent()/handleDone()/handleMerge() to push MCP notifications
- `cmd/oro/cmd_start.go` — Add --mcp flag, wire into buildDispatcher()
- `cmd/oro/root.go` — Register newMcpCmd() via AddCommand (~line 24)
- `go.mod` — Add `github.com/mark3labs/mcp-go`

**Files created:**
- `pkg/mcp/server.go` — MCP server implementation with DispatcherAPI interface
- `pkg/mcp/resources.go` — Resource handlers
- `pkg/mcp/tools.go` — Tool handlers
- `pkg/mcp/notifications.go` — Event notification emitter (called by dispatcher hooks)
- `pkg/mcp/server_test.go`
- `cmd/oro/cmd_mcp.go` — CLI for standalone MCP server (read-only SQLite adapter)

**What we're NOT doing:**
- No OAuth/JWT authentication for MCP. Local stdio and localhost HTTP are sufficient for v1. Auth as follow-up when remote workers exist.
- No MCP client registry/marketplace. Oro is a server, not a client marketplace.
- No MCP resource subscriptions (SSE streaming). Polling via resources + notifications is sufficient.

---

### Pattern 5: IDE Bridge (VS Code Extension)

**Source:** Claude Code's `src/bridge/` (20+ files, bidirectional WebSocket/SSE, JWT auth, permission proxying).

**Integration point:** If Pattern 4 (MCP) is implemented, VS Code can consume oro's MCP server natively — VS Code has built-in MCP client support. The bridge becomes an MCP consumer, not a custom protocol.

**What changes:**

1. **VS Code extension** (separate repo: `oro-vscode`) that connects to oro's MCP server and renders:
   - **Swarm Panel** — TreeView showing: dispatcher state, worker list (with context %, current bead), queue depth
   - **Bead Detail** — WebviewPanel showing AC, labels, priority, worker assignment, review history
   - **Notifications** — VS Code notifications on bead completion, escalation, review failure
   - **Commands** — `oro.submitBead`, `oro.focusEpic`, `oro.pauseSwarm`, `oro.scaleWorkers`

2. **Real-time updates** — MCP notifications push events; extension re-fetches resources on notification.

3. **Status bar** — Compact status bar item: `oro: 4/4 workers | 12 beads/hr | $2.40/hr`

**Files created:**
- New repo: `oro-vscode/` (TypeScript, VS Code Extension API)
- `oro-vscode/src/extension.ts`
- `oro-vscode/src/swarmPanel.ts`
- `oro-vscode/src/beadDetail.ts`
- `oro-vscode/src/mcpClient.ts`

**What we're NOT doing:**
- No JetBrains plugin (v1 is VS Code only).
- No inline diff visualization (workers don't produce diffs for human review — they merge directly).
- No permission proxying (workers are autonomous, not human-approved).
- No custom WebSocket protocol. MCP over HTTP is the transport.

**Prerequisite:** Patterns 1 (metrics for status bar data) and 4 (MCP server for all data/actions).

---

## Dependency Graph

```
Pattern 1 (Observability) ─────────────────┐
    ├── Pattern 3 (Containers) ──────────── │ ──→ Pattern 5 (IDE)
    │       └── needs /healthz from P1      │         └── needs MCP from P4
    │                                       │             + metrics from P1
Pattern 2 (Audit) ─────────────────────────┘
    └── Cost limits need P1's cost tracking

Pattern 4 (MCP) ────────────────────────────→ Pattern 5 (IDE)
    └── needs audit_log from P2 (for oro://audit resource)
```

**Critical path:** P1 → P3 → P5 (observability → containers → IDE)
**Parallel track:** P2 (audit) and P4 (MCP) can start immediately alongside P1.

## Token/Performance Budget

| Pattern | Binary size delta | Runtime overhead | New dependencies |
|---------|-------------------|------------------|-----------------|
| P1 Observability | +2MB (prometheus client) | ~1% CPU (metric increments) | `prometheus/client_golang` |
| P2 Audit | +0 (pure SQLite) | Negligible (INSERT per decision) | None |
| P3 Containers | +0 (build artifact) | N/A | Docker, Helm (build-time only) |
| P4 MCP | +1MB (mcp-go) | ~2% CPU (server goroutine) | `mark3labs/mcp-go` |
| P5 IDE | +0 (separate repo) | N/A | N/A (VS Code extension) |

Total new Go dependencies: 2 (`prometheus/client_golang`, `mark3labs/mcp-go`). Both are well-maintained, widely used.

## What We're NOT Doing (Global)

- **No React/Ink terminal UI.** Bubbletea is right for Go. Invest in IDE instead.
- **No single-agent coordinator mode.** Oro's dispatcher-worker is architecturally superior.
- **No vendor-specific analytics.** Prometheus + Grafana are the standard.
- **No feature flag dead-code elimination.** Go build tags suffice.
- **No 512K LOC sprawl.** Each pattern adds <2K LOC to oro.
- **No voice mode, plugin marketplace, or web UI.** These are consumer features, not enterprise infra.
- **No distributed SQLite.** Single-writer dispatcher = single SQLite. Correct for the architecture.

## Adversarial Review Findings (R1) — Fixed

Review verdict: FAIL. Three critical wiring gaps, all addressed:

| Finding | Severity | Fix Applied |
|---------|----------|-------------|
| Cost pipeline broken — DonePayload has no cost fields, data dies in worker | Critical | Added P1 step 5: extend DonePayload, wire worker→dispatcher cost pipeline |
| logEvent() is bare SQL INSERT with no observer hook (123 call sites) | Critical | Added MetricsObserver interface pattern — single-line change in logEvent(), not 123 site edits |
| HTTP server has no start path in Run() | Critical | Specified Run() startup, shutdownWithTimeout() cleanup, bind-failure behavior |
| TCP transport breaks sendStartDirective/pollForSocket (hardcode unix dial) | Important | Added CLI tooling updates + transport config file |
| MCP server has no access path to dispatcher internals | Important | Added DispatcherAPI interface + dual entry paths (standalone + embedded) |
| Missing audit migration constant for existing DBs | Important | Added MigrateAuditLog to schema.go changes |
| root.go not in any Files modified (new commands) | Minor | Added to P2 and P4 Files modified |
| go.mod not mentioned | Minor | Added to P1 and P4 Files modified |
| Makefile not mentioned for grafana assets | Minor | Added to P1 and P3 Files modified |

### R2 Minor Findings (noted, not blocking)

1. **logEventLocked()** — Duplicate of logEvent() with 7 call sites across dispatcher.go and worker_pool.go. Also needs MetricsObserver call. Fix: add same observer line, or refactor both to share a common core.
2. **Cost accumulation semantics** — Single-invocation CostUSD is available from ActivityResult. Cross-handoff cumulative cost requires dispatcher-side per-bead map (mentioned in P2). P1 should wire per-invocation cost; P2 handles accumulation.
3. **InputTokens/OutputTokens availability** — May not exist in claude CLI stream-json output. Spec should verify claude CLI JSON schema. If unavailable, drop from DonePayload and derive from CostUSD + model pricing table.
4. **TCP transport breaks oro stop/status/directive** — All dial UDS. Add to P3 Files modified: `cmd_stop.go`, `cmd_status.go`, `cmd_directive.go` must respect TransportMode from config file.

## Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|------------|
| Prometheus client adds memory pressure at scale (50+ workers × 20 metrics) | Medium | Use `prometheus.NewRegistry()` (not default), limit cardinality on worker_id labels |
| TCP transport introduces network failure modes UDS doesn't have | High | TCP transport is opt-in (default stays UDS). Add reconnection with exponential backoff (worker already has this). |
| TCP transport has no authentication — any network client can connect as worker | High | Default bind to 127.0.0.1 only. Non-localhost requires explicit --tcp-addr. No auth in v1 (documented risk). Auth required before production multi-node. |
| MCP server exposes dispatcher internals to external tools | Medium | Localhost-only by default. No auth in v1 = acceptable for single-machine. Add auth before remote workers. |
| Cost tracking accuracy depends on claude CLI output format | Medium | Parse best-effort, emit `oro_cost_parse_errors_total` counter. Cost limits are approximate, not billing-grade. |
| go:embed fallback + ConfigMap mount creates two asset paths | Low | Single `LoadAssets()` function. Test both paths. ConfigMap path checked first, embed is always available. |

## Execution Order

```
Week 1-2:  P1a — Metrics package + HTTP server + /healthz /readyz
Week 2-3:  P1b — Instrument logEvent(), cost tracking, Grafana dashboards
Week 2-4:  P2a — Audit table + audit() method + oro audit CLI
Week 3-5:  P2b — Cost limits + parameterized settings
Week 4-6:  P3  — Dockerfile + Helm + TCP transport + LoadAssets()
Week 3-7:  P4  — MCP server (resources, tools, notifications)
Week 8-12: P5  — VS Code extension (separate repo, MCP consumer)
```
