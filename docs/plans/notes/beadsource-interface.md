# BeadSource Interface Snapshot

Date: 2026-04-28
Bead: oro-ia1f
Source: `pkg/dispatcher/dispatcher.go:78-95`

## Current Source

The current `pkg/dispatcher.BeadSource` interface has 15 methods:

```go
// BeadSource provides ready work items. Production impl shells out to `bd ready`.
type BeadSource interface {
    Ready(ctx context.Context) ([]protocol.Bead, error)
    InProgress(ctx context.Context) ([]protocol.Bead, error)
    Blocked(ctx context.Context) ([]protocol.Bead, error)
    Closed(ctx context.Context, limit int) ([]protocol.Bead, error)
    Show(ctx context.Context, id string) (*protocol.BeadDetail, error)
    Close(ctx context.Context, id string, reason string) error
    Create(ctx context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error)
    Update(ctx context.Context, id, status string) error
    Sync(ctx context.Context) error
    AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
    HasChildren(ctx context.Context, epicID string) (bool, error)
    FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)
    Export(ctx context.Context) ([]byte, error)
    Defer(ctx context.Context, id, until string) error
    Undefer(ctx context.Context, id string) error
}
```

## Line References

| Line | Method |
|---:|---|
| 80 | `Ready(ctx context.Context) ([]protocol.Bead, error)` |
| 81 | `InProgress(ctx context.Context) ([]protocol.Bead, error)` |
| 82 | `Blocked(ctx context.Context) ([]protocol.Bead, error)` |
| 83 | `Closed(ctx context.Context, limit int) ([]protocol.Bead, error)` |
| 84 | `Show(ctx context.Context, id string) (*protocol.BeadDetail, error)` |
| 85 | `Close(ctx context.Context, id string, reason string) error` |
| 86 | `Create(ctx context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error)` |
| 87 | `Update(ctx context.Context, id, status string) error` |
| 88 | `Sync(ctx context.Context) error` |
| 89 | `AllChildrenClosed(ctx context.Context, epicID string) (bool, error)` |
| 90 | `HasChildren(ctx context.Context, epicID string) (bool, error)` |
| 91 | `FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error)` |
| 92 | `Export(ctx context.Context) ([]byte, error)` |
| 93 | `Defer(ctx context.Context, id, until string) error` |
| 94 | `Undefer(ctx context.Context, id string) error` |

## Implementation Cross-Check

`pkg/dispatcher/beadsource.go` implements all 15 methods on `CLIBeadSource`:

- `Ready`: line 42
- `InProgress`: line 58
- `Blocked`: line 77
- `Closed`: line 96
- `Show`: line 114
- `Close`: line 255
- `Defer`: line 264
- `Undefer`: line 273
- `Update`: line 284
- `Create`: line 303
- `Sync`: line 338
- `HasChildren`: line 344
- `FindByParentAndTag`: line 361
- `Export`: line 380
- `AllChildrenClosed`: line 391

## Drift Notes

The replatform spec's §2.2 historical snapshot says "13 BeadSource methods" and
lists lines 79-93, but current source is 15 methods at lines 79-95. The two
additional legacy methods are:

- `Defer(ctx context.Context, id, until string) error`
- `Undefer(ctx context.Context, id string) error`

The target `pkg/beadstore.Store` in spec §8.2 is still intentionally reshaped
to 12 methods and is not byte-identical to this legacy interface. Phase 1 should
explicitly account for deferred-bead behavior when replacing the legacy
`BeadSource` surface; otherwise `Defer` and `Undefer` are an implementation
gap, not just prose drift.
