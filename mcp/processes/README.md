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
- `start`, `runs`, `run_get`, `run_update`, `run_cancel`
- `step_get`, `step_update`

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

For single-agent direct work (no structured steps), read `run_get(process_id, run_id)` before domain actions.
Use `run_update` to report running/waiting/blocked/completed/failed/cancelled,
progress, current_step, result, and error. Only the run's original assignment
owner can update it, including from other threads. Completion requires result
evidence; blockers/failures need a reason. Terminal outcomes are immutable.
Tasks-backed runs use Tasks tools for progress and completion.

`runs` returns `direct_runs`, live Tasks `runs`, and durable `dispatches`.
Records include assignment identity and the original assignment snapshot.
`tasks_error` reports unavailable Tasks history; `has_more` indicates that Tasks
has older records beyond its 200-record response limit.

## Collaborative workflows

Leave `steps` empty for the existing single-agent behavior. To coordinate several
agents within one occurrence, define work and approval steps in **Procedure**,
then bind each role in **Assignments**. The assignment owner remains the run
coordinator. Roles can map to different agents, the same agent, or a human project
operator. Unbound work roles use the coordinator; roles used by any approval step
default to human review.

For example, a daily Patreon procedure can define research → write → review →
publish. Photography and Cooking assignments reuse these steps with their own
page parameters, schedules, coordinators, and role bindings.

```json
{
  "steps": [
    {"key":"write","name":"Write draft","role":"writer","kind":"work",
     "instructions":"Write a post for the configured page.",
     "expected_output":"Complete draft text","depends_on":[]},
    {"key":"review","name":"Review draft","role":"reviewer","kind":"approval",
     "instructions":"Check the draft against the publishing policy.",
     "expected_output":"Decision with reason","depends_on":["write"]},
    {"key":"publish","name":"Publish","role":"publisher","kind":"work",
     "instructions":"Publish the approved draft.",
     "expected_output":"Published URL","depends_on":["review"]}
  ]
}
```

Assignment configuration accepts `roles`, for example:

```json
{"writer":{"kind":"agent","agent_id":7},
 "reviewer":{"kind":"human"},
 "publisher":{"kind":"agent","agent_id":8}}
```

Steps without dependencies start together. Joins wait for every dependency.
Approval steps require an explicit `approved` or `rejected` decision; only
approval releases downstream work. Completed outputs and decisions are immutable.
The run freezes the procedure, role bindings, and parameters at creation. Each
step receives its instructions and all completed ancestor outputs.

Agents read `step_get(process_id, run_id, step_id)` before acting, then report
`step_update` with state, progress, output, error, and (for approvals) decision.
Only the assigned agent can update an agent step. Human steps are completed in
**Runs** by authenticated project operators; agents cannot approve as a human.
Completion requires nonempty evidence; waiting/blocked/failed/cancelled reports
require a reason. Core becoming idle does not complete a step. `run_update`
cannot bypass a structured workflow: its outcome derives from its steps.

Tasks remains optional. With Tasks execution selected, each ready agent work
step gets its own Tasks record and reports completion through Tasks. Approval
steps always record their decisions in Processes. Structured schedules always
belong to Processes, including when work steps use Tasks. Scheduled occurrences
avoid overlap per assignment. Tasks status is reconciled every five seconds.

The Runs panel shows executors, dependencies, outputs, decisions, and step task
links. It also lets the coordinator/operator call `run_cancel` with a reason.
Cancellation stops future handoffs; work already dispatched may still finish.
Pausing a process or assignment stops future scheduled occurrences, not a run.

Migration 004 adds `process_step_runs`, append-only `process_step_events`, and a
workflow flag on runs. Existing single-agent runs retain their original behavior.
Step deliveries retry stable IDs after restarts without changing their executor.

## HTTP

The SDK and platform authenticate operators. Routes require a project header or
query matching the installation's scope. Route IDs cannot be overridden in JSON.

- `GET/POST /processes`
- `GET/PUT /processes/{process}`
- `POST /processes/{process}/activate|pause|archive|start`
- `GET /processes/{process}/runs`
- `GET /processes/{process}/runs/{run}`
- `GET/POST /processes/{process}/runs/{run}/steps/{step}`
- `POST /processes/{process}/runs/{run}/cancel`
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
`processes/v0.4.0`. This release changes Processes only.

Structured approval steps enforce downstream handoffs. Free-text approval
requirements remain guidance. These gates do not revoke an agent’s general
external tool permissions or verify its reported evidence against external apps.
Human roles mean any authenticated project operator, not a named person; audit
records identify them as `operator`. Rejection fails the run; rework loops are
not supported yet. Correct the inputs/procedure and start a new run. External effects need integration-specific
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
legacy migration, real sidecars with and without Tasks, and panel interactions, plus role handoffs, parallel joins, frozen approval
outputs, rejected runs, workflow cancellation, and scoped executor authorization.


Tier 3 live-LLM smoke tests are in [scenarios/README.md](scenarios/README.md).
Run `bun run scenarios/run.ts` from this app directory to use Codex /
`gpt-5.6-terra` and verify persisted direct-run, assignment, and approval-gate
outcomes. These are separate from the deterministic Go integration suite.
