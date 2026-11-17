# Processes 0.20.0

A simpler agent protocol. Workers now get everything for their step up front and
report once; agents see fewer, merged tools.

## Workers: one brief, one write

- An isolated step worker's directive (its brief) now holds the step
  instructions, shared instructions, completion criteria, approval requirements,
  parameters, run inputs and the results of all completed earlier steps (direct
  dependencies first, older ancestors labelled "earlier"; 3 KiB per field, 6 KiB
  of results). The worker no longer has to call `step_get` or `step_read` first.
- The worker finishes with one `processes_step_update`: `state` plus `outcome`
  (the exact result, or the reason when blocked/waiting/failed). `outcome` is
  required in the schema, because some models drop optional fields deep in a
  long context. The server stays lenient: `outcome` only fills `output`, `error`
  or the handoff when the caller left them empty, so callers that still send
  `output`/`error` behave as before. `run_update` gets the same `outcome` field
  (fills `result`, `error` or `current_step`).
- Isolated workers get 4 Processes tools (`step_get`, `step_read`,
  `step_evidence_save`, `step_update`) instead of 13; the 6 memory tools are added
  only when the procedure uses memory. Persistent workers also get `step_claim`.
- Forgiving reads: an independent worker that calls `step_claim` gets its step
  back instead of an error, and `step_read` with `target=dependency` and no key
  returns all completed ancestors.
- Runs already in flight keep working: a step whose saved delivery envelope
  predates this release is delivered with its original message.

## Merged status tools (agent-visible change)

Agents now see 43 tools instead of 49:

| Before | Now |
| --- | --- |
| `processes_activate` / `pause` / `draft` / `archive` | `processes_status` with `status` |
| `processes_assignment_activate` / `pause` / `archive` | `processes_assignment_status` with `status` |
| `processes_trigger_activate` / `trigger_pause` | `processes_trigger_status` with `status` |
| `processes_assignment_get` | `processes_assignments` with `assignment_id` |

Each still runs the same internal action, with the same validation, locking and
events. The old names are not kept as aliases: an agent that calls one gets
"unknown tool" and sees the new tool in its list. The HTTP API and the panel are
unchanged.

## Removed

- The unused native-tasks subsystem (no registered tools or callers) and the
  unused `step_assign` path. Saved legacy task rows and foreign keys are kept.
- Migration 030 drops the superseded `process_worker_start_scans` table.

## Docs and tests

- The agent skill is down from 25 KB to 7 KB; per-release notes moved from the
  README into `CHANGELOG.md`.
- Tier 3 now runs 10 core scenarios by default (8 more in `scenarios/extended/`),
  with a compare script and a verify-only mode. On Codex `gpt-6.1-terra`, 0.19.4
  and 0.20.0 both pass all 10 including saved-state checks; 0.20.0 used 10% fewer
  iterations, 19% fewer tokens and 16% fewer Processes calls overall, and about
  35% fewer iterations and 40% fewer tokens on the two multi-agent workflows.
  Details in `scenarios/README.md`.
- Validated with `GOWORK=off go test ./...`, 209 Bun tests and the TypeScript
  check. No Core, Server or SDK changes; no UI changes.
