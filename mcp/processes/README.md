# Processes

Reusable company procedures, configured assignments, and independent execution
runs with durable native run and step history. Processes is self-contained;
Evals is an optional integration for testing immutable revisions before activation.

## Model

- **Process:** shared instructions, parameter definitions, operating policy,
  completion criteria, and immutable procedure versions.
- **Assignment:** a configured agent, parameter values,
  schedule, and procedure version policy.
- **Run:** one occurrence with a snapshot of the assignment, resolved parameters,
  original owner, procedure version, delivery identity, and outcome.
- **Step run:** an executable procedure step attached to a run. Processes stores
  its state, progress, output, evidence, retries, and delivery history.

For example, one “Publish a Patreon post” procedure can have Photography and
Cooking assignments with different page IDs, languages, agents, and daily times.
The same idempotency key can be used independently on each assignment.
Assignment-specific inputs belong in declared process parameters; assignments
do not have a separate page, client, or business field.

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

- `list`, `get`, `create`, `update`, `activate`, `pause`, `draft`, `archive`
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
Worker reads and claims contain the assigned step/checkpoint, one `dependencies`
manifest with complete ancestor receipts, and shared frozen policy. They omit
unrelated steps and delivery diagnostics. Retain shared policy and pass
`include_context=false` on later reads/claims to omit repeated policy, inputs,
parameters and assignment details; omit the flag (default true) to recover
context. `context_ref` identifies the frozen procedure and assignment revisions.
Updates return only IDs, accepted state/revision/progress, run state, `done`,
`next_action`, and any blocker reason. Follow `next_action`: continue current work
after progress; after completion await the next event or call native done when
`done=true`. Exact receipts remain saved and recoverable with `step_get`. Operator
HTTP reads and explicit `run_get` retain full inspection snapshots.

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
Migration 013 freezes the complete delivery envelope and worker provisioning
request before network calls. Retries replay exactly the same agent, thread,
event identity, message, and provisioning request after a lost acknowledgement
or restart, even if deadlines, dependencies, or app-generated context changed.
Unchanged reconciliation produces no row mutations or WAL writes. Global worker
callbacks only process the SDK-dispatched project, not all projects per call.

## HTTP

The SDK and platform authenticate operators. Routes require a project header or
query matching the installation's scope. Route IDs cannot be overridden in JSON.

- `GET/POST /processes`
- `GET/PUT /processes/{process}`
- `POST /processes/{process}/activate|pause|draft|archive|start`
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
schedule, and assignment revision. Legacy procedure owner/mode/
schedule fields remain readable in historical definitions. New versions omit
them, and round-tripping those fields cannot change an assignment. Creating a
procedure never creates a default assignment.

Migration 003 creates one default assignment per existing process and links old
runs to it. It preserves deadlines, pending synchronization, legacy identifiers,
original owners, procedure versions, and idempotency keys. Migration 010 adopts
legacy assignments into native execution without creating a new run.
Migration 017 removes the obsolete top-level assignment target label from saved
configurations and run snapshots while preserving parameter values. Existing
delivery envelopes remain byte-for-byte unchanged for safe retries.

Direct delivery uses stable tracked agent event IDs and pinned threads. Retries
back off from 30 seconds to 15 minutes for transient errors, including unavailable
agents, and recover at the next due retry once the agent is running. Warnings stay
visible throughout backoff. HTTP 409 conflicts suspend automatic delivery and
remain visible as **repair required**, including known conflicts migrated from
older releases. They are not automatically reassigned or given new event IDs:
the original delivery may already have executed. An operator must compare the
original platform event with the persisted envelope and establish what executed
before repairing the record; do not clear suspension or create a replacement
identity blindly. A valid lifecycle acknowledgement can confirm the original
delivery and resolve its warning without redelivery.

Direct recurring deadlines advance in the
same transaction that creates the occurrence. Missed intervals are skipped and
overlap is prevented per assignment; independent assignments can run together.
Core settling is diagnostic information, not business completion.

Synchronization retries every 30 seconds; native scheduling and delivery are
checked every 5 seconds. Processes has one native execution path.

## Installation and limits

Apteva >=0.52.0; app-sdk v0.90.0. Evals >=0.5.9 is optional. Source manifest pins
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

## Worker continuity

Assignments expose `worker_continuity` in HTTP/MCP and **Worker threads** in
its editor. The choice is frozen in each run's assignment snapshot:

- `auto` (default, including historical assignments): reuse the existing worker
  only for a strict, untimed same-agent chain; other steps remain isolated.
- `per_executor`: one persistent worker thread per run and executor agent, even
  with branches, dependency joins, redundant dependencies, or timed steps. This
  retains conversational context and discovered tools. Ready work on the same
  executor is serialized; different executor agents can run concurrently.
- `isolated`: provision a separate worker thread for each step, preserving
  parallel execution even when steps use the same agent.

Dependencies and timers still control eligibility. A reserved step, including
an ambiguous delivery or blocked step, prevents another step from being sent to
its worker. Each dispatch retains a separate tracked execution ID and immutable
delivery envelope; each step retains its own checkpoint and receipt. Workers
claim the app-assigned step before acting and use durable dependency evidence
for exact artifact identities. The final validation and human approval gates
are unchanged. A worker finishes after its own frozen steps are terminal, or
when the run becomes terminal, and stays available across waits while it still
has future assigned work. App restarts reuse saved ownership and delivery IDs.

Migration 014 preserves existing worker rows and changes their key to
`(run_id, agent_id)`. Existing assignment edits only affect future runs; they
never retarget previously dispatched step events. The app pins SDK v0.95.0,
the latest published tag by commit ancestry when this change was implemented.

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
needed during the delay. In automatic mode, timed workflows use independent step deliveries and untimed
strict sequential workflows reuse their existing worker. With `per_executor`,
timers wake the same saved worker thread after the delay. Restarts retain timers;
if the app was offline when due, it dispatches after recovery. Retries reuse the
same delivery identity. Cancellation stops future handoffs; pausing an assignment
only stops new runs. Start times are earliest eligibility, not guaranteed delivery
or completion times.

Live flows, the project map, and the overview widget show scheduled timing
and deadlines. Procedure-relative deadlines cannot be overridden on an individual
task. Date-parameter anchors, business calendars, reminders and escalation rules
are not part of this release. No Server or Core changes are required.

## MCP response recovery

`list` returns procedure discovery metadata with exact owner IDs and reread references.
`get` returns current procedure content once and historical version metadata;
`get(version=...)` returns the selected immutable definition and procedure metadata.
Historical definitions are loaded only when requested.

`run_get` retains the immutable definition and exact run/step state, outputs and
human approval attribution. MCP responses omit the duplicate textual snapshot and
step definitions already present in the procedure. `run_update` returns a compact
receipt with saved IDs, procedure version, state/progress, completion and blockers.
It does not echo the saved result. Step reads/updates, procedure metadata/version
entries and run reads/updates include `reread: {tool, args}` references to existing
tools with exact recovery arguments. Step recovery includes shared context.

These projections apply at the MCP boundary. HTTP/UI objects, stored definitions
and receipt evidence retain their existing behavior; no model summaries or
arbitrary truncation are used.

### AI-directed parallel steps

Assignments can opt in with `worker_continuity: "per_executor"` and
`parallel_execution: "auto"`. The persistent worker receives Core's normal
subthread capabilities and decides which independent ready steps benefit from
parallel work. Processes does not choose a fixed thread arrangement. Existing
assignments retain sequential execution for each persistent executor.

`max_parallel_steps` bounds unfinished claimed steps per executor (default 4,
range 1–8). Eligible steps are delivered to the same owner; delivery is not a
claim. Each claim checks dependencies, scheduled starts, exact owner and capacity
under the app's mutation lock. Running, waiting and blocked claims consume slots.
Step reads and acknowledgements include compact `ready_steps` and `active_steps`
identities, states and revisions without repeated instructions or output receipts.
Use step_get/step_claim with the returned IDs for exact context recovery.

The worker claims before performing or delegating work, keeps shared setup, and
passes children only the relevant frozen instructions, exact dependency evidence
and transferable references. Thread-bound sessions and shared mutable resources
stay sequential. Children report results through Core; only the durable owner can
save each step's separate output. Progress output can checkpoint child/operation
identities. On recovery the owner inspects those children and saved evidence before
retrying, preserving pending work. Validation, human approval and final completion
remain authoritative Processes gates. The limit bounds claimed process steps,
not the number of Core subthreads; thread structure remains the worker's choice.

Tier 3 scenario `scenarios/12-ai-parallel-steps.yaml` uses GPT-6.1 Sol and a local
fixture to verify actual operation overlap, exact separate receipts, retained
owner-only session checkpoints, real operator HTTP approval and publication.

### Step-by-step runs

Use **Run step by step** on an active assignment, or choose **Step by step** in
its run dialog. This prepares the frozen structured run without sending ready
work. Run detail shows ready steps and saved outputs; **Run this step** or **Run
next step** releases one, while **Run selected steps** releases chosen branches
with separate durable requests. Automatic **Run now** remains the default.

MCP callers pass `control_mode: "step_by_step"` to `processes_start`.
`processes_run_get` exposes `run.control_mode`, `eligible_steps`, `active_steps`,
and `waiting_for_advance`. Advance an exact eligible step with:

```json
{"process_id":"…","run_id":"…","step_id":"…","idempotency_key":"release-1"}
```

Call `processes_run_advance` from the coordinator's default thread, or use
`POST /processes/{process_id}/runs/{run_id}/advance` from the operator UI.
Workers and their children cannot release steps. Reuse the same key after a lost
reply; a key cannot select a different step. The compact response includes the
saved release, remaining eligible/active identities, delivery warnings and a
`processes_run_get` reread reference. Release authorization is committed before
network dispatch, and existing delivery envelopes retain their exact identities.

Ready status alone grants no execution authority in this mode. Unreleased steps
cannot be claimed or updated, and reconciliation/restart does not dispatch them.
Dependencies, timing, worker ownership and capacity still apply to released work.
Human steps must first be released, then separately completed by the project
operator with evidence; advancing an approval step does not approve it. Finishing
one step makes successors eligible and leaves them held for the next review.
The mode is frozen in the run binding; later assignment edits cannot change it.
Optional assignment `control_mode` sets the default for future starts, while a
start argument overrides it for that occurrence. Direct runs have no individual
step records and therefore cannot use step-by-step control.

Tier 3 scenario `scenarios/13-step-by-step.yaml` uses GPT-6.1 Sol, real MCP
advancement, HTTP branch releases and operator approval. Its independent verifier
checks stored authorizations, held-state observations, exact receipts, actual
parallel overlap, validation dependencies and publication gating.

### Publishing and returning to draft

Use **Publish process** beside the status at the top of process details.
Publishing enables active assignments to start manual, scheduled or triggered
runs; paused assignments remain paused. **Return to draft** stops new runs
and makes the procedure editable after synchronization. Existing runs continue
with their frozen definitions, and all versions, assignments and history remain.
Returning to draft does not create a version; **Save draft** creates the next
immutable version. The same action is available as MCP `draft` and HTTP
`POST /processes/{process}/draft`. **Pause process** remains available for
temporarily stopping new runs without returning to draft.

### Bounded history and cross-run memory

MCP `runs` returns one compact `runs` collection: default 10 rows, maximum 50,
with a 16 KiB response budget, exact IDs/frozen version/assignment/state,
agent-authored checkpoints and drill-down references. It queries metadata rather
than hydrating all executions. Filters include assignment, status (including
ongoing/attention groups), inclusive RFC3339 dates and an opaque keyset cursor.
Cursors bind to the filters and normalize fractional timestamps without changing
stored dates. Count and byte limits can both end a page; follow `next_cursor`.

`summary_get` and `summary_update` recover/save versioned semantic checkpoints
(4 KiB JSON maximum). Checkpoints capture progress, findings, outcomes, blockers,
next actions and exact evidence references; the app records author and revision.
Stable checkpoint keys make identical retries idempotent and optimistic revisions
prevent overwrites. Frozen procedure `summary_fields` configure optional labels
and metrics through MCP or the procedure editor. Missing historical checkpoints
are explicitly unavailable. Summaries never complete steps or provide approval.

`memory_list` and `memory_upsert` provide scoped, searchable, paginated ledger
entries (2 KiB maximum each), keyed by process, frozen assignment, campaign/scope
and stable source key. Each records its source run/step and author. Identical
retries preserve provenance, conflicting revisions fail, and other step executors
must name their assigned source step. Actual leads/assets stay in their source
apps; ledger entries reference them. New revisions publish compact app events
without including semantic contents or repeating evidence.

`run_get` supports `section=run|definition|steps|all` and a 48 KiB budget. Large
sections return `complete=false` and exact `run_evidence` references. Evidence
also supports `section=context` for exact shared execution fields without unrelated
step definitions or run aggregates. It uses UTF-8 byte offsets, a SHA-256, completion flags and a next reference; concatenate
JSON pages in order and restart if the hash changes. Oversized worker claims/reads
preserve a complete dependency manifest and deferred frozen context references.
All required instructions, receipts and approval evidence must be retrieved before
action. There is no model-generated replacement or silently shortened evidence.

HTTP full run inspection objects retain their previous shape. The UI opts into
`view=compact`, pages lightweight history, and loads the selected run on demand;
checkpoints and scoped knowledge render separately from activity. Explicit HTTP
`view=export` returns full inspection objects page by page with the same filters
and cursor. Legacy HTTP history remains available to existing inspection clients.

Tests cover multi-MB history, size limits, exact numeric IDs and Unicode evidence,
keyset dates/pagination/filter isolation, frozen configuration, restart recovery,
optimistic concurrent updates, retry/event deduplication and migration fidelity.
The tier 3 `14-run-memory.yaml` scenario runs two real GPT-6.1 Sol workers: the
second reads the first checkpoint/ledger, rotates discovery, preserves exact
receipts, and passes independent HTTP human approval. The verifier checks saved
state and telemetry independently of the model's final message.


### Targeted edits

MCP `patch` creates a new immutable draft revision from only the fields that
change. Supply `process_id`, `expected_version` and `changes`. Existing process
pause/synchronization checks, full-definition validation and version conflicts
apply. Omitted fields remain unchanged. Arrays/nested fields replace their whole
value. Steps and parameters support `add`, `update`, and `remove` by stable key;
new items append without reordering unchanged items. Remove referenced steps
only with explicit dependency edits in the same patch. Each key may occur once.

```json
{"process_id":"…","expected_version":3,"changes":{"steps":{"update":[{"key":"research","instructions":"Updated research instructions"}]}}}
```

The receipt returns saved version/status, changed field/key names and an exact
`processes_get` reference. Historical definitions and in-flight runs stay frozen;
follow-latest assignments adopt the new draft version and pinned assignments
keep their selected version. Use `update` for intentional full replacements.
The tier 3 `15-process-patch.yaml` scenario verifies real GPT-6.1 Sol partial
edits, stale rejection, preserved history and compact receipt recovery.
