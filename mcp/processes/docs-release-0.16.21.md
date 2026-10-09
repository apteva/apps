# Processes 0.16.21

Small procedure edits now use `processes_patch`: send only changed definition
fields or add/update/remove steps and parameters by stable key. The server
validates the whole result and saves one immutable draft version. Version checks
reject stale edits, existing pause/synchronization requirements apply, and failed
patches save nothing. Unchanged fields, historical revisions and in-flight runs
retain their exact content. Receipts contain changed field/key names and an exact
version reread reference; `processes_update` remains the full-replacement tool.

Previous-run discovery is compact and paginated by default through MCP. Filters,
query-bound cursors and a 16 KiB response budget avoid loading full execution
history. Explicit bounded run reads and hashed UTF-8 evidence pages recover exact
frozen definitions, states and outputs. HTTP inspection retains full objects;
UI history uses compact lists and lazy detail loading, with explicit paged export.

Agents can save versioned run checkpoints and scoped cross-run knowledge with
exact provenance, optimistic concurrency and idempotent writes. Configurable
summary fields appear in the editor/history, and checkpoints and knowledge render
in the UI. Summaries and memory do not replace execution receipts or approval.
Migration 018 adds their durable storage while preserving existing history.

The Processes app now pins app-sdk v0.99.0, verified as the latest published
ancestor of origin/main. Only the app dependency pin changed; SDK, Core and Server
sources were not modified.

Verification:
- Release-candidate GPT-6.1 Sol tier 3 patch scenario passed in 7 iterations and
  54.791 seconds with 148,468 reported tokens. Independent stored-state/tool-trace
  checks confirmed single-step edits, mixed edits, preserved immutable versions,
  stale rejection and compact receipt recovery. Report:
  `/private/tmp/processes-patch-release-0.16.21-tier3/run-nCWHoH`.
- Earlier GPT-6.1 Sol scenarios passed for two-run memory recovery, MCP response
  recovery, executor continuity, actual parallel work and step-by-step control.
- All 148 UI/scenario unit tests and 44 Playwright browser tests passed.
- Go short suite, real sidecar integration suite and targeted race tests passed
  with GOWORK=off against the published SDK pin. Regressions cover frozen runs,
  exact identifiers, atomic failures, stale/concurrent edits, restart recovery,
  bounded history/evidence and versioned checkpoints/knowledge.
- TypeScript, production panel build/import checks and manifest/source consistency
  passed. A synthetic single-step patch uses a 173-byte request versus a 40,940-byte
  full definition, with a 291-byte receipt; this is a payload measurement.

Release changes are confined to Processes and its marketplace metadata.
