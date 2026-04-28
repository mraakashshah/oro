# Baseline metrics

Bead: `oro-2o2a`

Captured: 2026-04-28T07:13:39Z

Environment:

- Repo: `/Users/as21/codehouse/oro`
- Worktree used for measurement: `.worktrees/oro-2o2a`
- bd: `bd version 1.0.2 (Homebrew)`
- Oro binary for dispatcher startup: throwaway build at `/tmp/oro-baseline`
  from commit `1e78c8e2`

The bead's B1-B5 acceptance labels are operational pre-flight baselines. They
are narrower than §18.1's final success-metric labels, but they provide the
current queue and dispatcher measurements requested by Phase 0.

## Results

| Metric | Value | Method |
| --- | ---: | --- |
| B1 bead count | 1677 | `bd list --json --limit 0 --all \| jq 'length'` |
| B2 ready count | 6 | `bd ready --json --limit 0 \| jq 'length'` |
| B3 in-progress count | 1 | `bd list --status=in_progress --json --limit 0 \| jq 'length'` |
| B4 avg Ready() latency | 106.79 ms | Mean of 10 `bd ready --json --limit 0` subprocess calls, parsing JSON each time |
| B5 dispatcher startup time | 594.36 ms | Throwaway `oro start --daemon-only --workers 0 --max-workers 0`; elapsed until Unix socket was connectable |

## Raw B4 samples

Milliseconds for 10 sequential `bd ready --json --limit 0` calls:

```text
112.01, 104.26, 103.22, 113.04, 108.07, 107.72, 106.94, 104.52, 101.94, 106.16
```

Summary:

- Average: 106.79 ms
- Minimum: 101.94 ms
- Maximum: 113.04 ms

## B5 methodology

Built a temporary binary after staging embedded assets for compilation:

```bash
./scripts/build-assets.sh
go build -o /tmp/oro-baseline ./cmd/oro
```

Startup measurement used isolated daemon paths so it would not connect to or
disturb any existing Oro process:

```bash
ORO_SOCKET_PATH=<temp>/oro.sock \
ORO_PID_PATH=<temp>/oro.pid \
/tmp/oro-baseline start --daemon-only --workers 0 --max-workers 0
```

The timer started immediately before process spawn and stopped when the socket
file existed and accepted a Unix-domain socket connection. The process was then
stopped with SIGINT. Captured stdout/stderr summary:

```text
starting dispatcher (PID 29986, workers=0)
dispatcher stopped
assets updated from v0.1.0-287-g2db89209-dirty to v0.1.0-303-g1e78c8e2-dirty
shutdown: received interrupt (PID 29986)
```

The assets message is expected for a throwaway worktree build and does not
affect the committed repo state.
