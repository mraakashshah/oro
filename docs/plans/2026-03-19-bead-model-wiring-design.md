# Bead Model Override Wiring — Design Doc

**Date:** 2026-03-19
**Goal:** Wire per-bead model overrides from bd metadata to ResolveModel()
**Scope:** `pkg/protocol/types.go`, `pkg/dispatcher/beadsource.go`, `pkg/dispatcher/beadsource_test.go`, `cmd/oro/cmd_work.go`

---

## Problem

Users can set model overrides on beads via `bd update <id> --set-metadata model=opus`, but the Go code never reads them. bd stores model in `metadata.model` (nested JSON object). bd's JSON output does not include a top-level `model` field — the value lives only in the `metadata` map. Standard JSON unmarshaling into `Bead.Model` (`json:"model"`) never sees it.

Result: `ResolveModel()` always falls through to estimate-based routing. The explicit override path (`b.Model != ""`) is dead code.

## Design

### Approach: Extract metadata.model in beadsource post-processing

Add a `Metadata` field to `Bead` for deserialization, then extract `model` from it after unmarshaling. This is done in `beadsource.go` rather than custom UnmarshalJSON to keep the Bead struct simple and avoid unmarshaling side effects.

### Changes

**1. Add Metadata field to Bead and BeadDetail** (`pkg/protocol/types.go`):
```go
Metadata map[string]any `json:"metadata,omitempty"` // bd custom metadata
```

Must be `map[string]any` (NOT `map[string]string`) because bd preserves native JSON types in metadata values (e.g., `--set-metadata count=42` produces `{"count": 42}` as a JSON number). Using `map[string]string` would cause `json.Unmarshal` hard failures on any bead with non-string metadata values.

This field is added to BOTH `Bead` and `BeadDetail` structs. All 5+ unmarshal sites in beadsource.go (Ready, InProgress, Show, HasChildren, FindByParentAndTag, AllChildrenClosed) will gain this field. Only Ready/InProgress/Show need post-processing; the others just ignore it.

**2. Add extractMetadataModel helper** (`pkg/dispatcher/beadsource.go`):
```go
func extractMetadataModel(beads []protocol.Bead) {
    for i := range beads {
        if beads[i].Model == "" && beads[i].Metadata != nil {
            if m, ok := beads[i].Metadata["model"].(string); ok && m != "" {
                // Only accept known model values to prevent typos from propagating
                switch m {
                case protocol.ModelOpus, protocol.ModelSonnet, protocol.ModelHaiku:
                    beads[i].Model = m
                }
            }
        }
    }
}
```

Call after `json.Unmarshal` in `Ready()` and `InProgress()`.

**3. Same for Show()** — add `extractMetadataModelDetail` for BeadDetail:
```go
func extractMetadataModelDetail(detail *protocol.BeadDetail) {
    if detail.Model == "" && detail.Metadata != nil {
        if m, ok := detail.Metadata["model"].(string); ok && m != "" {
            switch m {
            case protocol.ModelOpus, protocol.ModelSonnet, protocol.ModelHaiku:
                detail.Model = m
            }
        }
    }
}
```

**4. Wire `oro work` standalone path** (`cmd/oro/cmd_work.go`):
After loading bead via `Show()`, honor the bead's model override when no explicit `--model` flag was given:
```go
if cfg.model == protocol.DefaultModel && detail.Model != "" {
    cfg.model = detail.Model
}
```

### Why not custom UnmarshalJSON?

Custom UnmarshalJSON on Bead would affect ALL JSON deserialization (tests, protocol messages, serialization roundtrips). The metadata extraction is specific to bd CLI output, so it belongs in beadsource.go where bd output is processed.

### User workflow

```bash
# Set model override
bd update oro-abc --set-metadata model=opus

# Verify
bd show oro-abc --json | jq '.[0].metadata.model'
# → "opus"

# Dispatcher picks up override via bd ready --json → extractMetadataModel → ResolveModel()
```

## Test Plan

- `TestExtractMetadataModel_PopulatesFromMetadata` — bead with `Metadata["model"]="opus"` gets `Model="opus"`
- `TestExtractMetadataModel_PreservesExplicitModel` — bead with explicit `Model="haiku"` is not overwritten by metadata
- `TestExtractMetadataModel_EmptyMetadata` — bead with nil metadata → Model stays empty
- `TestExtractMetadataModel_InvalidModel` — metadata.model="gpt4" → Model stays empty (not in allowlist)
- `TestExtractMetadataModel_NonStringValue` — metadata.model=42 (wrong type) → Model stays empty
- `TestCLIBeadSource_Ready_ExtractsMetadataModel` — raw bd JSON with `"metadata":{"model":"opus","count":42}` → bead.Model="opus", unmarshal succeeds
- `TestCLIBeadSource_Ready_MixedMetadataTypes` — raw bd JSON with non-string metadata values → unmarshal succeeds, model extracted correctly

## Adversarial Review (2026-03-19)

**Round 1:** 3 blocking issues found and fixed:
- B1: `map[string]string` breaks on non-string metadata values (bd preserves native JSON types). Fixed: use `map[string]any` with type assertion.
- B2: `oro work` standalone path ignores `detail.Model`. Fixed: honor bead model when no `--model` flag.
- B3: Adding Metadata to Bead affects 5+ unmarshal sites. Fixed: documented all sites, confirmed `map[string]any` is safe.

5 non-blocking issues incorporated: model validation allowlist (N1), mock coverage gap note (N2), mixed-type test (N3), BeadDetail extraction explicit (N4), corrected "model: null" claim (N5).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| bd metadata format changes | Low | Medium | extractMetadataModel is defensive (type assertion + nil check) |
| Metadata field bloats Bead in UDS messages | Low | Low | omitempty; metadata is typically small |
| Invalid model value in metadata | Low | Low | Allowlist check: only accept opus/sonnet/haiku |
| Non-string metadata values | Confirmed | N/A | Fixed: `map[string]any` handles all JSON types |

## Dependencies

None. Additive change. Existing behavior preserved when metadata is absent or model not set.
