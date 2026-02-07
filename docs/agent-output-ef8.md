## oro-ef8: Directive Types for Protocol Package

### Files Created
- `pkg/protocol/directive.go` — `Directive` type (string enum), `Command` struct
- `pkg/protocol/directive_test.go` — 3 test functions covering constants, validation, JSON round-trip

### Test Results
```
=== RUN   TestDirectiveConstants
--- PASS: TestDirectiveConstants (0.00s)
=== RUN   TestDirectiveValid
--- PASS: TestDirectiveValid (0.00s)
=== RUN   TestCommandJSON
--- PASS: TestCommandJSON (0.00s)
PASS
ok   oro/pkg/protocol  0.166s
```

### Design Decisions
- **Directive as `type Directive string`**: Provides type safety while keeping SQLite compatibility (stored as plain strings).
- **Command.Directive field is `string`, not `Directive`**: Matches the SQLite table schema where the column is a plain text value. Callers can cast to `Directive` and call `Valid()` when they need validation.
- **`Valid()` uses a switch statement**: Simple, no allocations, exhaustive against the four known constants.
- **JSON tags use lowercase field names**: Matches Go convention for JSON serialization (`id`, `ts`, `directive`, `target`, `processed`).
