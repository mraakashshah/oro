# P2f Review Retry Recovery

- **Time:** 2026-06-01T09:26:07Z
- **Symptom:** `oro-s08-p2f` repeatedly returned from review with the same rejected dispatcher reranker gating behavior.
- **Affected task/worker/branch:** `oro-s08-p2f`, `worker-1780291207894489000-0`, `agent/oro-s08-p2f`.
- **First bad observation:** the branch gated the existing `codesearch` Claude reranker behind the new `memory.semantic.rerank` flag, changing pre-existing dispatcher code-index behavior.
- **Suspected root cause:** the task implementation confused the new optional card cross-encoder slot with the existing code-search reranker and then retried without fully applying review feedback.
- **Evidence:** current branch showed `semanticRerankEnabledFromConfig(repoRoot)` wrapping `idx.SetReranker(...)`; quality gate also rejected `RerankEnabled` as a dead export after production use was removed.
- **Corrective action:** paused dispatcher, restarted the active worker to stop concurrent branch rewrites, restored unconditional code-index reranker wiring, kept card rerank fail-open/tail-preserve behavior, added `TopN: 0` no-op coverage, and marked the default-off accessor test-only until a later production consumer wires it.
- **Verification:** `go test ./pkg/cards/ -run 'TestRerank_(FailOpenPreservesTail|TopNZeroNoop)' -count=1 -v`, `go test ./pkg/langprofile/ -run TestSemanticMemoryConfig -count=1`, `go test ./cmd/oro/ -run 'TestStartReadsProjectConfig|TestStartRejectsRepoLocalOroShadow|TestDaemonSpawnerUsesResolvedSelfExecutable' -count=1`, and `./quality_gate.sh` passed in `.worktrees/oro-s08-p2f`.
- **Prevention:** when adding semantic memory rerank controls, verify the target subsystem before applying the flag; code-search reranking and card semantic-memory reranking are distinct paths.
