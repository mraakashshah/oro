# Agent Output: oro-6dq — UDS Message Types

## Files Created

- `pkg/protocol/message.go` — MessageType constants and payload structs
- `pkg/protocol/message_test.go` — TestMessageTypes and TestMessageJSON

## Test Results

```
=== RUN   TestMessageTypes
--- PASS: TestMessageTypes (0.00s)
=== RUN   TestMessageJSON
=== RUN   TestMessageJSON/ASSIGN
--- PASS: TestMessageJSON/ASSIGN (0.00s)
=== RUN   TestMessageJSON/SHUTDOWN
--- PASS: TestMessageJSON/SHUTDOWN (0.00s)
=== RUN   TestMessageJSON/HEARTBEAT
--- PASS: TestMessageJSON/HEARTBEAT (0.00s)
=== RUN   TestMessageJSON/STATUS
--- PASS: TestMessageJSON/STATUS (0.00s)
=== RUN   TestMessageJSON/HANDOFF
--- PASS: TestMessageJSON/HANDOFF (0.00s)
=== RUN   TestMessageJSON/DONE
--- PASS: TestMessageJSON/DONE (0.00s)
=== RUN   TestMessageJSON/READY_FOR_REVIEW
--- PASS: TestMessageJSON/READY_FOR_REVIEW (0.00s)
=== RUN   TestMessageJSON/RECONNECT
--- PASS: TestMessageJSON/RECONNECT (0.00s)
--- PASS: TestMessageJSON (0.00s)
PASS
ok  	oro/pkg/protocol	0.180s
```

## Design Decisions

1. **Flat envelope with pointer payloads**: `Message` struct uses `*XxxPayload`
   pointer fields with `omitempty`. This keeps JSON clean (only populated payload
   appears) and avoids the complexity of `json.RawMessage` or interface
   unmarshaling. Trade-off: the envelope grows one field per message type, but
   with 8 types this is trivial.

2. **Two const blocks**: Dispatcher-to-Worker and Worker-to-Dispatcher constants
   are grouped in separate `const` blocks for clarity.

3. **No methods**: Per the constraint "types and constants only," no methods
   were added beyond what `encoding/json` requires (exported fields with tags).

4. **RECONNECT.BufferedEvents uses `[]Message`**: This allows nested messages
   to use the same envelope type, which round-trips cleanly through JSON.
