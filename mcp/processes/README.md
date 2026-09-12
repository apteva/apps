# Processes

Company operating procedures that agents follow directly or through optional Tasks integration.
The app provides its own project-page React panel, eleven MCP tools, and a SQLite
store. It does not run domain work or call LLMs.

## Execution modes

New procedures default to `execution_mode: "agent"`. Processes sends the owner a
tracked agent event and stores progress and outcomes itself; no Tasks install is
needed. Select `"tasks"` to use Tasks 3.6.0+ for execution and scheduling. Existing
v0.1 definitions retain Tasks mode. Pause and confirm synchronization before
changing modes; old runs retain their original backend and procedure version.
There is no automatic backend fallback on delivery failure.

Direct schedules use durable deadlines and atomic occurrence creation. Missed
intervals are skipped, and an open scheduled run prevents overlap. Manual runs
can coexist. Deliveries retry with stable event IDs and pinned threads, with
30-second exponential backoff capped at 15 minutes. Core lifecycle settlement
is diagnostic information, never proof of business completion.

## Features

- Draft, active, paused, and archived procedures with an owner agent.
- Plain-language instructions, required input sources, standing context,
  approval checkpoints, and completion evidence.
- Immutable versions. Pause and confirm synchronization before editing; saving
  creates a new draft. Existing tasks retain their original procedure snapshot.
- On-demand runs and recurring interval/five-field cron schedules with IANA
  timezones. The selected backend owns deadlines, occurrences, and overlap policy.
- Overview, procedure/version editor, and live run history in the app-owned panel.
- Links from run history to Tasks, and from task snapshots back to their procedure.
- Durable dispatch keys and desired-state reconciliation every 30 seconds. Task
  creation retries reuse the original snapshot and key after failures/restarts.

Install with **Apteva >=0.50.4**. **Tasks >=3.6.0 is optional**. Both apps pin app-sdk
v0.79.0 (the latest ancestor-verified SDK release when published). Requires `db.write.app`,
`platform.apps.call`, `platform.instances.read`, `platform.instances.write`, and
`platform.threads.write` for tracked delivery. Tasks is declared optional.
The app registry advertises Processes. Source manifests pin the immutable
`processes/v0.2.0` and `tasks/v3.6.0` release tags.

## Agent tools

The manifest advertises the local names `list`, `get`, `create`, `update`,
`activate`, `pause`, `archive`, `start`, `runs`, `run_get`, and `run_update`. The host namespaces these as
Processes tools. MCP calls require trusted agent and project context.

Create/update accepts a `definition` object:

```json
{
  "execution_mode": "agent",
  "name": "Monthly financial close",
  "description": "Produce an approved financial summary.",
  "instructions": "Collect records, reconcile balances, investigate discrepancies, and prepare the report.",
  "required_inputs": "Invoices, payment records, previous closing balances.",
  "default_inputs": "Use the company reporting currency.",
  "completion_criteria": "Approved report with reconciled totals and supporting evidence.",
  "approval_requirements": "Finance lead approval before distributing the report.",
  "owner_agent_id": 7,
  "schedule": {"kind": "cron", "cron": "0 9 1 * *", "timezone": "Europe/Madrid"}
}
```

Omit `schedule` for on-demand work. Intervals use
`{"kind":"interval","every":"24h"}` (minimum 1m). `update` also requires
`process_id` and `expected_version` to reject stale saves. `start` requires a
stable `idempotency_key` and accepts optional plain-text `inputs`. Reusing a key
with different inputs is rejected. An accepted run is retried even if the
procedure is subsequently paused or archived; it was already requested work.

`get` returns current settings and version history; optional `version` returns
that historical definition. `runs` returns recent live Tasks records, their
procedure version, dispatch records, and `has_more` when history is truncated.
`direct_runs` contains Processes-owned run history. `tasks_error` reports
unavailable historical Tasks data without blocking direct history.

For direct runs, agents read `run_get(process_id, run_id)` and use
`run_update(process_id, run_id, state, progress?, current_step?, result?, error?)`.
States: running, waiting, blocked, completed, failed, cancelled. Only the immutable
owner can update the run. Completion requires result evidence; failures and
blockers require a reason. Terminal outcomes are immutable. Tasks-backed runs
use Tasks get/set_progress/assign/complete.

## HTTP panel API

All routes require authenticated project scope (`X-Apteva-Project-ID` or the
project-scoped gateway query). Mismatched header/query/install scope is rejected.
The SDK provides bearer authentication; the platform authenticates operators.

- `GET/POST /processes`: list or create a draft.
- `GET/PUT /processes/{id}`: read or save a revision.
- `POST /processes/{id}/activate|pause|archive|start`: lifecycle or execution.
- `GET /processes/{id}/runs`: direct and live Tasks history.

The panel is `/ui/ProcessesPanel.mjs`, declared with `slot: project.page`.
No dashboard-specific registration or server asset embed changes are needed.

## Data and reliability

Processes owns three tables:

| Table | Responsibility |
| --- | --- |
| `processes` | Project scope, current version, desired lifecycle, sync state, direct schedule deadlines |
| `process_versions` | Immutable definition JSON and author/timestamp |
| `process_runs` | Dispatch keys, immutable version references, backend, direct outcomes and delivery state, optional task IDs |

A schedule dispatch row points to its Tasks schedule definition; Tasks stores
individual occurrences. Processes reads those occurrences with their inherited
version through the private bridge, rather than duplicating their execution
state. Historical dispatch/version rows remain after archive.

Tasks adds `process_task_links` for authenticated install/project/process
provenance. Its `process_task` MCP tool is `app_only`, hidden from agents and
restricted to platform-authenticated Processes installs. It can create/read
linked work and pause/resume linked schedules; it cannot complete arbitrary
agent tasks. Inter-app calls use `CallAppResult`, not direct database access.

Recurring tasks are created **paused**, and their IDs are persisted before
activation. Reconciliation disables old schedule versions before enabling the
current one. Unknown creation outcomes retry the same durable key. A failed
lifecycle change returns HTTP 202 with `sync_pending` and `sync_error`; the UI
shows the unresolved state and offers retry. Pausing/archiving does not cancel
queued or running occurrences. Do not manage process schedules independently
through Tasks if you want the process lifecycle to remain authoritative.

## Deliberate limits

Approval and completion criteria are instructions and evidence requirements,
not enforced approval gates. This version has no workflow graph or per-step
execution engine. Inputs are text/source requirements, not validated form
schemas. Runs are assigned to the owner's configured default thread. History
shows up to 200 recent Tasks records and exposes truncation; older records
remain in Tasks. Publishing this release does not install it into projects or
restart production agents.

## Verification

From this directory (module-isolated commands also avoid a broken workspace
Go toolchain selection):

```sh
GOWORK=off go test -race ./...
GOWORK=off go test -race -tags integration ./...
GOWORK=off go build .
```

From `apps/`:

```sh
bun run scripts/build-panels.ts --app processes
bun run scripts/build-panels.ts --app tasks
```

Tests cover revision immutability, owner/project boundaries, private tool
exposure, duplicate/concurrent starts, lost responses, restart reconciliation,
paused schedule creation, actual scheduled occurrences, and live completion
history through both real sidecars. The integration test uses a local recording
platform and never contacts production agents.
