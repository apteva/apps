# Telephony 0.14.0

Adds an optional AI context lookup: an AI destination can load business facts
for the call from a Functions-app function before the agent speaks. See
[ai-context-lookup.md](ai-context-lookup.md).

- AI destinations accept `context: {function_id, timeout_ms, variables,
  on_failure}`, validated when the destination is saved and frozen per call.
- Telephony invokes `functions_invoke` while the call is prepared, with a hard
  deadline (default 1.5 s, at most 3 s) that cancels the platform call.
- The validated facts (a JSON object up to 4 KiB) reach the agent as a
  data-only `[CALL BUSINESS CONTEXT]` block after the directive, with
  `context_status` `ok` or `unavailable`. An optional `greeting` personalises
  the opening line.
- On timeout, error or an invalid response the agent starts without facts, or,
  with `on_failure: fallback`, the AI is skipped and the node's failure fallback
  runs (code `context_unavailable`).
- Lookups are stored per call and listed with routing decisions
  (`context_lookups`), and publish `telephony.ai_context.succeeded` /
  `telephony.ai_context.failed`. A startup retry reuses a successful lookup.
- Destinations without `context` behave exactly as before.

Also fixes a 0.13.0 audio edge: when audio for an already-cancelled reply was
queued behind a reply's last packet, that packet was sent without its 3 ms
fade-out, so the reply could still end on a hard cut. Packets that will be
dropped as cancelled no longer count as following audio, on Twilio and the other
carrier streams.

## Migration

`071_ai_context_lookups.sql` adds a new table only. `database_upgrade` remains
`backward_compatible`.
