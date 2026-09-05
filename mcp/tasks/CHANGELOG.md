# Tasks changelog

## 3.5.1 — 2026-09-05

Tasks now persists notifications with the task mutation and retries delivery
after failures or restarts. Stable delivery IDs prevent duplicate execution;
terminal changes suppress obsolete queued notifications. Network delivery runs
outside the scheduler lock with bounded concurrency.

- Fix schedule cancellation, stale scheduler snapshots, future one-time resume,
  interval phase preservation, and manual runs of paused schedules.
- Keep active work from being failed solely because progress is old. Preserve
  lifecycle ordering and prevent overlapping recovery attempts.
- Enforce terminal immutability and completion/failure evidence across HTTP and
  MCP mutations. Correct assignment and terminalization transitions.
- Filter operational work before limits, prioritize failures, and paginate task,
  event, and run lists. Add indexed overlap lookup and compact list payloads.
- Guard UI requests against stale responses, coalesce refreshes, surface errors,
  and improve dialog keyboard handling and focused task actions.
- Validate local one-time schedule dates and reject ambiguous DST inputs.
- Preserve legacy upgrade records, normalize stored timestamps without changing
  occurrence identity, and recover stranded delivery work at startup.
- Expose delivery and scheduler health through `/delivery-health`.

Validation: Go unit, integration, and race checks; Go vet; strict TypeScript;
17 UI tests; four rebuilt panels; and 19 live LLM scenario definitions passing
across validation runs, including all three repetitions of each stress scenario.
The scenario runner must recognize successful Core `done(message)` as well as
`send(message)` responses; see `TESTING.md`.

Upgrade notes: migrations apply automatically. Records already erased by an
older migration require an existing backup. Audit and deduplication history is
retained; reads are bounded, with no automatic destructive purge. No comparative
load benchmark or LLM-token savings measurement was performed.
