# Processes

Reusable company procedures, configured assignments, and independent execution
runs with durable native run and step history. Processes is self-contained;
Evals is an optional integration for testing immutable revisions before activation.

## Model

- **Process:** shared instructions, parameter definitions, operating policy,
  completion criteria, and immutable procedure versions.
- **Assignment:** a saved target (page, client, business), agent, parameter values,
  schedule, and procedure version policy.
- **Run:** one occurrence with a snapshot of the assignment, resolved parameters,
  original owner, procedure version, delivery identity, and outcome.
- **Step run:** an executable procedure step attached to a run. Processes stores
  its state, progress, output, evidence, retries, and delivery history.

For example, one “Publish a Patreon post” procedure can have Photography and
Cooking assignments with different page IDs, languages, agents, and daily times.
The same idempotency key can be used independently on each assignment.

## Panel

The project-page panel has **Processes**, **Runs**, and **Project map** areas.
The project Runs browser lists recent and ongoing execution across every process,
with state filters and a selected detail view. Each process also has Overview,
Procedure, Assignments, Triggers, and Runs tabs; its Runs tab uses the same compact
list-and-detail layout.

Run details show native step state, evidence, timing, and delivery
history. Agent steps also expose their execution-scoped tool calls on demand,
including the tool's `_reason`, success/failure state, and the providing app or
integration icon. Correlation uses the durable execution ID, so sequential steps
sharing a persistent worker thread do not mix their tool activity.

The **Processes overview** dashboard widget shows active runs, blocker
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
time, which the UI labels explicitly. No external execution history is queried;
all run and step state is read from Processes. No execution or schedule settings
change on an overview read.

- Create an unassigned procedure with no agent or schedule, even in a project with no agents.
- Configure execution later in Assignments; saving a procedure never creates an assignment.
- Define text, number, and yes/no parameters, required fields, and defaults.
- Add and edit assignments with independent agents, schedules, and parameters.
- Unavailable coordinators and role agents are shown explicitly. Select replacements
  before saving; roles without an explicit agent follow the new coordinator.
- Start, pause, activate, or archive one assignment without changing the others.
- Override parameters for one manual run without modifying the assignment.
- Filter run history by assignment, agent, or outcome. All execution history is
  native to Processes.
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
- `step_claim`
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
`runs` returns native run and step history plus durable `dispatches`.
Records include assignment identity and the original assignment snapshot.

## Visual process editor

Overview and Procedure show generic steps as connected cards. In
the editor, add steps, select a card to edit its instructions, role and required
output, and drag between its ports to set dependencies. Connections mean every
predecessor must finish; cycles are rejected. Select a connection to remove it,
or use the inspector’s dependency checklist. Processes derives a deterministic
layout from those dependencies, so agents and operators never author or persist
graph coordinates. Fit flow resets zoom. Historical coordinates remain readable
for compatibility but are ignored. General instructions and settings are below
the canvas.

The MCP authoring contract follows the same model. `validate_definition` checks
and normalizes semantic procedure content without writing. `create` saves an
unassigned draft. `assignment_create` separately selects executors and always
starts paused. Process activation, assignment activation, and starting a run are
separate actions that require explicit authorization. Readiness metadata and the
Overview/Procedure readiness card summarizes steps, required parameters, and
assignments. Approval requirements are ordinary frozen policy for agents to follow.

## Collaborative workflows

Leave `steps` empty for the existing single-agent behavior. To coordinate several
agents within one occurrence, define generic steps in **Procedure**,
then bind each role in **Assignments**. The assignment owner remains the run
coordinator. Roles can map to different agents, the same agent, or a human project
operator. Every unbound role uses the coordinator; human execution must be chosen
explicitly in the assignment.

For example, a daily Patreon procedure can define research → write → review →
publish. Photography and Cooking assignments reuse these steps with their own
page parameters, schedules, coordinators, and role bindings.

```json
{
  "steps": [
    {"key":"write","name":"Write draft","role":"writer",
     "instructions":"Write a post for the configured page.",
     "expected_output":"Complete draft text","depends_on":[]},
    {"key":"review","name":"Review draft","role":"reviewer",
     "instructions":"Check the draft against the publishing policy.",
     "expected_output":"Review result with evidence","depends_on":["write"]},
    {"key":"publish","name":"Publish","role":"publisher",
     "instructions":"Publish the reviewed draft.",
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
Every step completes with output evidence; completed outputs are immutable.
If approval is required, describe it in the step instructions or procedure policy
and let the assigned agent use the appropriate tool or communication channel.
The run freezes the procedure, role bindings, and parameters at creation. Each
step receives its instructions and all completed ancestor outputs.

Agents read `step_get(process_id, run_id, step_id)` before acting, then report
`step_update` with state, progress, output, and error.
Only the assigned agent can update an agent step. Human steps are completed in
**Runs** by authenticated project operators; agents cannot approve as a human.
Completion requires nonempty evidence; waiting/blocked/failed/cancelled reports
require a reason. Core becoming idle does not complete a step. `run_update`
cannot bypass a structured workflow: its outcome derives from its steps.

Processes is the sole execution and history system. The Runs panel shows
executors, dependencies, outputs, and step delivery details. It also
lets the coordinator/operator call `run_cancel` with a reason.
Cancellation stops future handoffs; work already dispatched may still finish.
Pausing a process or assignment stops future scheduled occurrences, not a run.

Migration 004 adds `process_step_runs`, append-only `process_step_events`, and a
workflow flag on runs. Migration 010 replaces legacy task event names and
normalizes future execution to the native runtime. Existing historical rows
remain readable.
Step deliveries retry stable IDs after restarts without changing their executor.

## HTTP

The SDK and platform authenticate operators. Routes require a project header or
query matching the installation's scope. Route IDs cannot be overridden in JSON.

- `GET/POST /processes`
- `GET/PUT /processes/{process}`
- `POST /processes/{process}/activate|pause|archive|start`
- `GET /processes/{process}/runs`
- `GET /processes/runs`
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
target, schedule, and assignment revision. Legacy procedure owner/mode/
schedule fields remain readable in historical definitions. New versions omit
them, and round-tripping those fields cannot change an assignment. Creating a
procedure never creates a default assignment.

Migration 003 creates one default assignment per existing process and links old
runs to it. It preserves deadlines, pending synchronization, legacy identifiers,
original owners, procedure versions, and idempotency keys. Migration 010 adopts
legacy assignments into native execution without creating a new run.

Direct delivery uses stable tracked agent event IDs and pinned threads. Retries
back off from 30 seconds to 15 minutes. Direct recurring deadlines advance in the
same transaction that creates the occurrence. Missed intervals are skipped and
overlap is prevented per assignment; independent assignments can run together.
Core settling is diagnostic information, not business completion.

Synchronization retries every 30 seconds; native scheduling and delivery are
checked every 5 seconds. Processes has one native execution path.

## Installation and limits

Apteva >=0.52.0; app-sdk v0.85.0. Evals >=0.5.9 is optional. Source manifest pins
`processes/v0.15.0`. Event triggers retain the durable app subscription requirement
introduced in v0.5.0; see [platform requirements](docs-release-0.5.0.md#platform-requirement).
Evaluation requires the optional Evals app and isolated Environments support.
This release changes Processes only; Conversations is not modified.

The Processes overview widget supports both project Home and the global Home
(`All projects`). Global Home requires a separate global Processes installation;
the global installation aggregates only the records in its own database for
projects returned by `PlatformAPI().ListProjects()`. Existing project-scoped
installations are not implicitly copied or merged, so their history remains
available in the corresponding project Home. New work executed through the
global installation is stored with its project ID and appears in the global
overview. The global overview is read-only and supports a visible-project
selector.

Dependencies enforce downstream handoffs. Approval requirements remain frozen
procedure policy, not a Processes-specific gate or decision type. Human roles mean
any authenticated project operator, not a named person; audit records identify
them as `operator`. External effects need integration-specific
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
legacy migration, native execution without external work apps, and panel interactions,
plus role handoffs, parallel joins, generic human steps, workflow cancellation,
and scoped executor authorization.


Tier 3 live-LLM smoke tests are in [scenarios/README.md](scenarios/README.md).
Run `bun run scenarios/run.ts` from this app directory to use Codex /
`gpt-6-sol` and verify persisted direct-run, assignment, and generic-step
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
Unstructured runs remain visible in the inspector. The map has search, status and
live-only filters.

The read-only map uses the panel's existing event stream. Refresh preserves the
viewport, cancels stale requests, limits concurrent history reads to four, and
reports missing or truncated execution data while retaining all SOP boundaries.


Project and individual flows share Apteva theme colors, status icons, selection
outlines, and theme geometry. Neutral step outlines and connectors use moderate contrast, between the host
hairlines and the brighter v0.11.2 treatment. Terminal and Clean themes are checked
in both light and dark modes. Running/ready work uses
the host accent; completed, waiting/blocked, and failed work use semantic colors.
Motion respects the reduced-motion preference.

## Step timing

Each structured step can define an optional `start_after` and `due_after` rule.
`start_after` holds work until the earliest start time; `due_after` is a completion
deadline, which flags overdue work without delaying or cancelling it. Rules use
`after: "run_start"` or `after: "step_completed"` with a `step_key` referencing a
step dependency or its ancestor. Offsets are nonnegative whole `minutes`, `hours`,
or `days`, at most 365 days; a day is 24 hours. All dependencies must still
finish even if the start time has already passed.

For “send an email now, then send a follow-up 10 minutes later,” define two steps,
make the second depend on the first, and add this to the second:

```json
{
  "start_after": {"after":"step_completed","step_key":"send_first","offset":10,"unit":"minutes"},
  "due_after": {"after":"step_completed","step_key":"send_first","offset":30,"unit":"minutes"}
}
```

The editor exposes these under **Timing**. Agents can create the same rules via
`create`/`update`. Definitions and timing rules are frozen with each run. Resolved
`start_at`, `due_at`, and the first successful `completed_at` are persisted in
SQLite. A completion-relative timer starts when Processes records successful
completion, not when an external service says it performed the action. Report
the first email's completion promptly.

The app's five-second worker changes eligible steps from `scheduled` to `ready`
and sends the normal tracked event to their assigned agent or requests human
work. No model requests or sleeping worker are
needed during the delay. Timed workflows use independent step deliveries; untimed
sequential workflows still reuse their existing worker. Restarts retain timers;
if the app was offline when due, it dispatches after recovery. Retries reuse the
same delivery identity. Cancellation stops future handoffs; pausing an assignment
only stops new runs. Start times are earliest eligibility, not guaranteed delivery
or completion times.

Live flows, the project map, and the overview widget show scheduled timing
and deadlines. Procedure-relative deadlines cannot be overridden on an individual
task. Date-parameter anchors, business calendars, reminders and escalation rules
are not part of this release. No Server or Core changes are required.
