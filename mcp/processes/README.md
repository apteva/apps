# Processes

Reusable company procedures, configured assignments, and independent execution
runs. Agents can execute directly, or use the optional Tasks app for tracking.

## Model

- **Process:** shared instructions, parameter definitions, approval guidance,
  completion criteria, and immutable procedure versions.
- **Assignment:** a saved target (page, client, business), agent, parameter values,
  schedule, execution mode, and procedure version policy.
- **Run:** one occurrence with a snapshot of the assignment, resolved parameters,
  original owner, procedure version, delivery identity, and outcome.
- **Task:** optional Tasks-backed work linked to a run. Direct runs need no Tasks
  installation. A Tasks schedule creates its occurrences in Tasks; Processes
  reads their results live and associates them with their assignment.

For example, one “Publish a Patreon post” procedure can have Photography and
Cooking assignments with different page IDs, languages, agents, and daily times.
The same idempotency key can be used independently on each assignment.

## Panel

The project-page panel has Overview, Procedure, Assignments, and Runs tabs.

- Create a procedure and its convenience default assignment in one form.
- Define text, number, and yes/no parameters, required fields, and defaults.
- Add and edit assignments with independent agents, schedules, and parameters.
- Start, pause, activate, or archive one assignment without changing the others.
- Override parameters for one manual run without modifying the assignment.
- Filter run history by assignment, agent, or outcome. Only Tasks records link
  to Tasks. Direct history remains available when Tasks history is unavailable.
- Follow the latest procedure version, or pin an assignment to a selected version.

Pause and confirm synchronization before editing an active assignment. Pausing
or archiving the whole process stops future schedules for all assignments.
Already requested runs continue, including pending delivery retries. Individual
assignment pause choices survive process pause/reactivation.

Procedure edits currently require pausing the process and confirming schedule
synchronization. Saving creates a new draft version. Assignments that follow
latest advance to that version, while pinned assignments stay unchanged.
Activate the process to resume enabled assignments. New required parameters
must be configured before activation. Runs already created never change.

## Agent API

MCP requires trusted agent/project context. Tools are scoped to a process in
that project. The host namespaces these local tool names:

- `list`, `get`, `create`, `update`, `activate`, `pause`, `archive`
- `assignments`, `assignment_get`, `assignment_create`, `assignment_update`,
  `assignment_activate`, `assignment_pause`, `assignment_archive`
- `start`, `runs`, `run_get`, `run_update`

Procedure definitions accept a `parameters` array:

```json
[
  {"key":"page_id","label":"Patreon page","type":"string","required":true},
  {"key":"language","label":"Language","type":"string","default":"en"},
  {"key":"paid_only","label":"Paid members only","type":"boolean","default":true}
]
```

String fields may also declare an `options` array. Unknown keys, invalid types,
invalid options, and missing required values are rejected when executing.
Use connection references and external resource IDs, never stored credentials.
Parameters are data provided alongside the procedure; they do not grant rights
or create permission to act on an external resource.

Create an assignment with `process_id` and `assignment`:

```json
{
  "name":"Photography Patreon",
  "target":"Photography page",
  "owner_agent_id":7,
  "execution_mode":"agent",
  "follow_latest":true,
  "parameters":{"page_id":"photo-page","language":"en","paid_only":true},
  "schedule":{"kind":"cron","cron":"0 9 * * *","timezone":"Europe/Madrid"}
}
```

Assignments are created paused. Activate the process, then activate the desired
assignments. `assignment_update` also requires `assignment_id` and
`expected_revision`; edits preserve its paused/enabled intent. Set
`follow_latest:false` and `procedure_version` to pin a specific version.

`start` takes `process_id`, `assignment_id`, a stable `idempotency_key`, optional
`parameters` overrides, and optional free-text `inputs`. Omitting assignment_id
works only when exactly one non-archived assignment exists. Retries must use the
same assignment, key, input text, and explicit overrides. They reuse the saved
snapshot even after assignment or procedure changes.

For direct work, read `run_get(process_id, run_id)` before domain actions.
Use `run_update` to report running/waiting/blocked/completed/failed/cancelled,
progress, current_step, result, and error. Only the run's original assignment
owner can update it, including from other threads. Completion requires result
evidence; blockers/failures need a reason. Terminal outcomes are immutable.
Tasks-backed runs use Tasks tools for progress and completion.

`runs` returns `direct_runs`, live Tasks `runs`, and durable `dispatches`.
Records include assignment identity and the original assignment snapshot.
`tasks_error` reports unavailable Tasks history; `has_more` indicates that Tasks
has older records beyond its 200-record response limit.

## HTTP

The SDK and platform authenticate operators. Routes require a project header or
query matching the installation's scope. Route IDs cannot be overridden in JSON.

- `GET/POST /processes`
- `GET/PUT /processes/{process}`
- `POST /processes/{process}/activate|pause|archive|start`
- `GET /processes/{process}/runs`
- `GET/POST /processes/{process}/assignments`
- `GET/PUT /processes/{process}/assignments/{assignment}`
- `POST /processes/{process}/assignments/{assignment}/activate|pause|archive|start`

Create/update bodies wrap the definition in `definition` or configuration in
`assignment`. Updates require expected_version or expected_revision. Lifecycle
operations return HTTP 202 when schedule synchronization is pending.

## Storage, migration, and reliability

SQLite stores `processes`, immutable `process_versions`, `process_assignments`,
and `process_runs`. Run snapshots preserve resolved parameter values, agent,
backend, target, schedule, and assignment revision. Legacy procedure owner/mode/
schedule fields remain as API compatibility defaults for the first assignment.

Migration 003 creates one default assignment per existing process and links old
runs to it. It preserves deadlines, pending synchronization, task IDs, original
owners, procedure versions, and idempotency keys. Existing Tasks schedules are
reused, not recreated. No new execution is triggered by migration.

Direct delivery uses stable tracked agent event IDs and pinned threads. Retries
back off from 30 seconds to 15 minutes. Direct recurring deadlines advance in the
same transaction that creates the occurrence. Missed intervals are skipped and
overlap is prevented per assignment; independent assignments can run together.
Core settling is diagnostic information, not business completion.

Tasks schedules are born paused. Their IDs are persisted before resume. A lost
resume response invalidates the previous pause confirmation, so switching modes
requires a fresh confirmed pause. Synchronization retries every 30 seconds;
direct scheduling/delivery is checked every 5 seconds. There is no automatic
fallback between execution backends.

## Installation and limits

Apteva >=0.50.4; app-sdk v0.79.0. Tasks >=3.6.0 is optional. Source manifest pins
`processes/v0.3.0`. This release changes Processes only.

Approval requirements remain instructions, not enforced approval gates. Result
evidence is agent-reported, not automatically verified against external apps.
Each run has one accountable agent; multi-agent step routing and multiple Tasks
per run are not part of this release. External effects need integration-specific
deduplication/reconciliation; event delivery alone cannot guarantee exactly-once
publication. Direct history is currently returned without pagination.

## Verification

```sh
GOWORK=off go test -race -tags integration ./...
```

From the apps repository root:

```sh
bun test mcp/processes/ui/ProcessesPanel.test.tsx
bun run scripts/build-panels.ts --app processes
```

Tests cover assignment isolation, snapshots across edits and retries, typed
parameters, independent schedule overlap/pause, version following and pinning,
legacy migration, real sidecars with and without Tasks, and panel interactions.
