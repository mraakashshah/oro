# Loom Learnings: Autonomous Agent Architecture

How Loom keeps agents moving without waiting for user prompts.

## 1. Event-Driven State Machine

The agent uses an explicit state machine (`crates/loom-common-core/src/agent.rs`) that enables autonomous operation.

**States:**
- `WaitingForUserInput` - Idle, ready for next task
- `CallingLlm` - Making LLM requests
- `ProcessingLlmResponse` - Handling responses
- `ExecutingTools` - Running tool calls in parallel
- `PostToolsHook` - Running post-tool operations (auto-commit)
- `Error` - Recoverable errors with retry
- `ShuttingDown` - Graceful shutdown

**Autonomous Flow:**
```
User Input → LLM Call → Process Response → Execute Tools → Post-Tools Hook → LLM Continues (loop)
```

The agent loops `tools → hook → next LLM call → tools` without user intervention until reaching a terminal state.

## 2. Background Job Scheduler

Location: `specs/job-scheduler-system.md` + `crates/loom-jobs/`

Periodic jobs run on tokio timers with configurable intervals. Each job runs in its own `tokio::task`.

```rust
async fn run_periodic_job(
    job: Arc<dyn Job>,
    interval: Duration,
    mut shutdown_rx: broadcast::Receiver<()>,
) {
    let mut interval_timer = tokio::time::interval(interval);
    loop {
        tokio::select! {
            _ = interval_timer.tick() => {
                execute_with_retry(&job, ...).await;
            }
            _ = shutdown_rx.recv() => break,
        }
    }
}
```

**Built-in Jobs:**
- Weaver Cleanup (30 min)
- Anthropic Token Refresh (5 min)
- Session Cleanup (1 hour)
- OAuth State Cleanup (15 min)
- Job History Cleanup (24 hours)
- Missed Run Detection (1 min)

## 3. Post-Tools Hook Architecture

After tool execution, agent transitions to `PostToolsHook` state, runs infrastructure operations (e.g., auto-commit), then automatically continues to next LLM call.

```
ExecutingTools (all tools done, mutating files)
    ↓
PostToolsHook (auto-commit, or other hooks)
    ↓
CallingLlm (send next request automatically)
```

## 4. Server Query Detection & Injection

Location: `specs/server-query-phase-2.md`

1. LLM generates response
2. `QueryDetectionStrategy` analyzes for query patterns
3. If pattern matched → pause LLM stream
4. Send ServerQuery to client
5. Receive response
6. `ResultFormatter` injects result back into conversation
7. LLM resumes with context automatically

**Pattern Examples:**
- "I need to read src/main.rs" → File read query
- "What's in the environment?" → Env var query

## 5. Cron Monitoring System

Location: `specs/crons-system.md` + `crates/loom-crons-core/`

- **Missed Run Detection**: Background job runs every minute checking for overdue monitors
- **Timeout Detection**: Automatically detects jobs exceeding `max_runtime_minutes`
- **Health Status Transitions**: Automatically updates monitor health without user action
- **Synthetic Events**: Creates "missed" or "timeout" check-ins automatically

```rust
pub async fn check_missed_monitors(db: &Database, sse: &SseBroadcaster) -> Result<()> {
    let overdue_monitors = sqlx::query!(
        "SELECT * FROM cron_monitors
         WHERE next_expected_at + margin < now() AND last_checkin_at < next_expected_at"
    ).fetch_all(db).await?;

    for monitor in overdue_monitors {
        create_checkin(CheckIn {
            status: CheckInStatus::Missed,
            source: CheckInSource::System,
            ..
        }).await?;
    }
}
```

## 6. Thread Persistence & Async Continuation

Location: `specs/thread-system.md` + `crates/loom-thread/`

- Threads save conversation state automatically
- Threads sync to server asynchronously (non-blocking)
- Agent can resume from saved state without user re-initialization

```json
{
  "agent_state": {
    "kind": "executing_tools",
    "pending_tool_calls": [...]
  },
  "conversation": { "messages": [...] }
}
```

## 7. Retry & Error Recovery

Automatic retry with exponential backoff:

```rust
const MAX_RETRIES: u32 = 3;
const BASE_DELAY: Duration = Duration::from_secs(5);

for retry_count in 0..MAX_RETRIES {
    match job.run(&ctx).await {
        Ok(_) => return Ok(...),
        Err(JobError::Failed { retryable: true, .. }) => {
            let delay = BASE_DELAY * 2u32.pow(retry_count);
            tokio::time::sleep(delay).await;
        }
        Err(e) => return Err(e),
    }
}
```

## Key Architectural Patterns

| Pattern | Enables |
|---------|---------|
| Event-driven state machine | Autonomous state transitions without user input |
| Background tokio tasks | Long-running periodic operations |
| Broadcast channels | Coordination without blocking |
| Async/await everywhere | Non-blocking I/O for continuous operation |
| Retry with backoff | Resilient autonomous recovery |
| Hook architecture | Infrastructure operations without main loop changes |
| Result injection | LLM continues with async results automatically |

## Key Files

- **State Machine**: `crates/loom-common-core/src/agent.rs`
- **State Definitions**: `crates/loom-common-core/src/state.rs`
- **Job Scheduler Spec**: `specs/job-scheduler-system.md`
- **Cron Monitoring Spec**: `specs/crons-system.md`
- **Thread System Spec**: `specs/thread-system.md`
- **Server Query Spec**: `specs/server-query-phase-2.md`

## Summary

Loom agents continue autonomously through:
1. Event-driven state machine that loops without user prompts
2. Background job scheduler for periodic autonomous tasks
3. Cron system with automatic missed-run detection
4. Async/await + tokio for non-blocking continuous operation
5. Retry mechanisms for automatic error recovery
6. Hook patterns for post-tool infrastructure operations

Core pattern: **async state machine + tokio tasks + broadcast channels**
