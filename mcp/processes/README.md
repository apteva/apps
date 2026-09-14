# Processes

Reusable company procedures, configured assignments, and independent execution
runs, with native tasks for both recurring and one-off work. Agents can execute
directly, or use the optional Tasks app for procedure execution.

## Model

- **Process:** shared instructions, parameter definitions, approval guidance,
  completion criteria, and immutable procedure versions.
- **Assignment:** a saved target (page, client, business), agent, parameter values,
  schedule, execution mode, and procedure version policy.
- **Run:** one occurrence with a snapshot of the assignment, resolved parameters,
  original owner, procedure version, delivery identity, and outcome.
- **Task:** an executable procedure step, standalone work item, or extra work
  attached to a run. Native tasks reuse step execution and need no Tasks app.
  The optional Tasks execution backend remains available for procedure work.

For example, one “Publish a Patreon post” procedure can have Photography and
Cooking assignments with different page IDs, languages, agents, and daily times.
The same idempotency key can be used independently on each assignment.

## Panel

The project-page panel has **Processes** and **Work** areas. Each process has
Overview, Procedure, Assignments, and Runs tabs. Work combines native tasks and
approvals across the project, with filters, creation, settings, and history.
Run workspaces also support adding required or optional tasks to an active run.
See [native tasks](docs-native-tasks.md) for the shared model and permissions.

The **Processes overview** dashboard widget shows active runs, approval/blocker
attention, upcoming assignments, and recent outcomes across the current project.
Its default All view orders running work before schedules and past/blocked runs.
Four equal-height rows show the process and assignment on the left and the
current step, status, and completed-step count on the right. Schedules show their
next run time and finished runs show their outcome;
select a row for live steps, agent names, and links to run or assignment details.
Visible rows can be configured from one to six. It is read-only and refreshes
through host event revisions without creating a stream or invoking a model.
A native mobile widget provides the same overview and live step summaries.

`GET /processes/overview` (also `/overview` and `/processes/mobile/overview`)
requires project context. Direct-run counts cover the project; each returned list
is capped at 12 and each run at 60 step rows. Recent outcomes sort by run creation
time, which the UI labels explicitly. Optional Tasks history uses authoritative
Tasks state, with at most eight procedure lookups and 200 records per procedure;
partial/unavailable history is disclosed instead of treating dispatch records as
live executions. No execution or schedule settings change on an overview read.

- Create an unassigned procedure with no agent or schedule, even in a project with no agents.
- Configure execution later in Assignments; saving a procedure never creates an assignment.
- Define text, number, and yes/no parameters, required fields, and defaults.
- Add and edit assignments with independent agents, schedules, and parameters.
- Unavailable coordinators and role agents are shown explicitly. Select replacements
  before saving; roles without an explicit agent follow the new coordinator.
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
that project, or to project-level native work. The host namespaces these local tool names:

- `list`, `get`, `create`, `update`, `activate`, `pause`, `archive`
- `assignments`, `assignment_get`, `assignment_create`, `assignment_update`,
  `assignment_activate`, `assignment_pause`, `assignment_archive`
- `start`, `runs`, `run_get`, `run_update`, `run_cancel`
- `step_get`, `step_update`
- `tasks`, `task_runs`, `task_create`, `task_get`, `task_update`, `task_cancel`

Native task updates use the current `expected_revision`; creation uses a stable
`idempotency_key`. Standalone tasks create no hidden procedure or run.
[Event triggers](docs-event-triggers.md) provide assignment event configuration,
preview, activation, and event history tools.

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

## Visual process editor

Overview and Procedure show work steps and approval gates as connected cards. In the editor, add steps, select a card to edit its instructions, role and required output, and drag between its ports to set dependencies. Connections mean every predecessor must finish; cycles are rejected. Select a connection to remove it, or use the inspector’s dependency checklist. Drag cards to arrange them, use Auto layout to restore dependency order, and Fit flow to reset zoom. Layout positions are stored with each immutable procedure version. General instructions and execution settings are below the canvas.

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
schedule fields remain readable in historical definitions. New versions omit
them, and round-tripping those fields cannot change an assignment. Creating a
procedure never creates a default assignment.

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

Apteva >=0.51.3; app-sdk v0.80.0. Tasks >=3.6.0 is optional. Source manifest pins
`processes/v0.6.0`. Event triggers retain the durable app subscription requirement
introduced in v0.5.0; see [platform requirements](docs-release-0.5.0.md#platform-requirement).
Native tasks require no additional server changes. This release changes Processes only.

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

The panel declares its host ReactDOM imports in `package.json` under
`apteva.panelExternals`; the shared panel builder keeps other apps’ import
contracts unchanged.

From the apps repository root:

```sh
bun test mcp/processes/ui/ProcessesPanel.test.tsx mcp/processes/ui/flow-model.test.ts
bunx playwright test --config mcp/processes/playwright.config.ts
bun test mcp/processes/ui/Work.test.tsx
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


## Project map

The Processes panel includes a Project map view for the whole project. One shared
canvas shows every SOP inside its own boundary, including every defined step and
dependency. Horizontal and vertical layouts pack the boundaries into a roughly
square overview, with shared pan, zoom, minimap, and Fit all SOPs controls.

Concurrent live runs of the current procedure version appear separately on each
step, labeled with assignment, run ID, state, and agent. Runs from older or unknown
versions, and runs without matching step tracking, have their own visible run cards
inside the SOP boundary, showing version, state and current work. Each live run is
accounted for on the canvas. Their original step snapshots remain available in the
execution inspector; they are never projected onto the latest definition. Select a SOP, step, or run to
inspect instructions and execution details, then open the existing SOP/run page.
Unstructured runs remain visible in the inspector. Recurring Tasks schedule records
are not counted as live executions. The map has search, status and live-only filters.

The read-only map uses the panel's existing event stream. Refresh preserves the
viewport, cancels stale requests, limits concurrent history reads to four, and
reports missing or truncated execution data while retaining all SOP boundaries.


Project and individual flows share Apteva theme colors, status icons, selection
outlines, and theme geometry. Neutral step outlines and connectors use moderate contrast, between the host
hairlines and the brighter v0.11.2 treatment. Terminal and Clean themes are checked
in both light and dark modes. Running/ready work uses
the host accent; completed, waiting/blocked, and failed work use semantic colors.
Motion respects the reduced-motion preference.
