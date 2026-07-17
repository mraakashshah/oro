# Bound standalone worker card decks

**Date:** 2026-07-17
**Component:** `pkg/worker` prompt assembly
**Severity:** high

## Symptom

`AssemblePrompt` rendered all relevant deck cards for a standalone worker. A
4,000-card deck produced a 1,867,099-byte prompt, which can exceed host
argument and worker-model input limits before the worker starts.

## Root Cause

`cardsBody` copied every non-inline deck card into a new slice and rendered the
entire slice. The dispatcher bounds assignment JSON, but standalone prompt
assembly had no corresponding output bound.

## Solution

Deck-view rendering is capped at 256 KiB. It retains the source-order prefix,
keeps inline cards untouched, and appends the exact number of omitted deck
cards. A summary that alone would exceed the remaining budget is clipped at a
UTF-8 rune boundary while preserving its card metadata and retrieval command.

## Prevention

Prompt sections fed by ranked collections must bound both retained output and
intermediate allocation. Add regression coverage with a large ordered input,
an omission-count assertion, and a Unicode oversized-value case.

## Related

- `pkg/dispatcher/assign_payload.go` bounds assignment card JSON before it is
  sent to a worker.
