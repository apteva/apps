# Native tasks in Processes

Processes owns a common task execution record. A procedure step creates a task
when its run starts; operators and agents can also create standalone tasks or add
tasks to an active run. These all appear in the panel's **Work** view.

| Origin | Parent | Completion behavior |
| --- | --- | --- |
| `process_step` | Run and frozen procedure step | Required; follows the procedure's dependencies and role binding |
| `standalone` | Project only | Independent work; creates no hidden process, assignment, or run |
| `attached` | Existing active run | Required or optional; never changes the reusable procedure |

Tasks can be work or approval tasks, assigned to an agent or an authorized human
project operator. Named human assignees are not supported. A due date is a work
deadline, not a scheduled start: ready agent work dispatches immediately.

## Shared execution

The existing `process_step_runs` table stores all three origins. Migration 006
makes `run_id` nullable and adds project scope, origin, required status, due date,
creation metadata, revision, and an idempotency key. Existing step IDs, frozen
definitions, results, delivery receipts, and audit history are preserved. The
existing `process_step_events` audit table also records creation and setting
changes. `StepRun` remains a source-compatible alias of `Task`; existing step
HTTP and MCP APIs continue to work.

The existing worker, tracked agent event delivery, stable event identity, retry
handling, dependency checks, state transitions, and output validation execute
native tasks. An idle agent or a delivery receipt does not complete a task. The
assigned executor must report its outcome and evidence. Approvals additionally
require an explicit approved or rejected decision.

Required attached tasks gate run completion. In structured runs, failure,
cancellation, or rejection of required work fails the run. In an unstructured
native run, the coordinator's completion request is rejected until required
tasks complete successfully; the coordinator reports the overall run outcome.
Optional tasks do not gate or fail the run and remain actionable after a run
completes. Failed or cancelled runs stop future task dispatch and outcome updates;
unresolved tasks can still be cancelled for cleanup. Cancellation cannot revoke
already dispatched work or external side effects.

An attached task may depend on existing task keys in the same run. Required work
cannot depend on optional work. Dependencies are immutable after creation.
Dependency outputs, frozen assignment parameters, and run inputs are available
to the assigned agent. These values are context data, not additional authority.

## Tools and HTTP

Agent-facing tools use the normal `processes_` prefix:

| Tool | Purpose |
| --- | --- |
| `tasks` | List work; defaults to the caller agent's tasks, with `assignee=all` for project work |
| `task_runs` | Find active runs that accept additional native tasks |
| `task_create` | Create standalone work, or attach work with `run_id` and `required` |
| `task_get` | Read current instructions, revision, dependencies, run context, permissions, and history |
| `task_update` | Record progress/outcome or edit allowed settings |
| `task_cancel` | Cancel with a reason |

List filters include assignee, state (`active` excludes terminal states), origin,
run, process, search, and overdue. Pagination uses `limit` (1–100) and `offset`.
HTTP equivalents under `/processes` are GET/POST `/tasks`, GET/PUT `/tasks/:id`,
POST `/tasks/:id/cancel`, and GET `/task-runs`. Project and caller identity come
from trusted request context, never the request body.

Creation requires a stable `idempotency_key`, scoped to project and creating
actor. Repeating identical inputs returns the same task, even after it completes;
different inputs using that key conflict. Task creation and its first audit entry
commit atomically before dispatch. Update/cancel requires `expected_revision`
from a recent `task_get`; stale updates conflict.

Only the assigned executor may report outcomes. The creator, run coordinator, or
project operator may edit settings; only the coordinator or operator may attach
tasks to a run. The assignee can also cancel their task. Reassignment and text
edits stop after any delivery attempt, lifecycle receipt, or execution start.
Due dates can still change on active work. Procedure task instructions remain
frozen; add an attached task to extend a single run instead.

## Panel and optional integration

The **Work** tab provides a unified filtered list, task creation, task details,
result/approval controls, settings, cancellation, and history. A run's task
workspace exposes the same list and composer scoped to that run. The composer
supports standalone or attached work, required/optional status, an agent or human
executor, a due date, and work or approval type. Dependency configuration is
available through MCP/API in this version.

Native tasks need no Tasks app installation or new server mechanisms. The
existing optional Tasks execution mode still applies to procedure-generated work
steps: those outcomes must be reported through their linked Tasks record. Ad hoc
tasks attached to structured runs remain native. Legacy unstructured Tasks-backed
runs cannot accept attached native tasks because their external Tasks record
owns run completion; use native execution or structured steps for that feature.

## Verification

Unit/race tests cover dispatch and retry, project/executor authorization,
revision conflicts, creation idempotency and audit atomicity, required/optional
completion, approval gates, lifecycle handling, and migration preservation. An
integration-tagged test boots the real sidecar, creates and completes work through
MCP, and verifies one agent dispatch with no inter-app Tasks calls or hidden
processes. React tests cover the shared list, creation retries, attachment,
optional completion after a run, and executor-specific controls. Browser checks
exercise desktop/mobile layout and task creation/completion/cancellation using a
mock API. These checks do not run a real LLM.
