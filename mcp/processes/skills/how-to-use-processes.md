# Processes

Processes defines immutable procedure revisions, assignments, schedules, event
triggers, native runs, and durable step-run history. A run is one occurrence of
an assignment. Every step is tracked by Processes with its own state, progress,
output, evidence, approvals, retry metadata, and revision.

## Execution

Create or update a procedure while it is a draft or paused. Create an
assignment with an owner agent, optional schedule, parameters, and step roles.
Activate the procedure and assignment, then start a run with a stable
`idempotency_key`.

Before acting, read `run_get` or `step_get`. Agents report meaningful progress
and the terminal outcome with `run_update` or `step_update`. Completion requires
concrete output and evidence. Approval steps require an explicit approved or
rejected decision.

For an agent step, Processes provisions an isolated worker through the platform
thread API. The worker receives the Processes coordination tools and inherits
the executor agent's spawnable MCP servers automatically, using the same
capability-inheritance path as Conversations. The worker reads the authoritative
step before any domain action and reports milestones and the terminal outcome
with `step_update`. Agents do not spawn workers or call `step_assign`; worker
creation, ownership, and the authoritative wake are app-owned and idempotent.

Processes is the sole execution and history system. Do not create a separate
task, forward work manually, or poll another app for step status. For structured
runs, wait for the Process event that delivers the next ready step after its
dependencies and timing rules are satisfied.

## Testing revisions

Evals is optional. When installed, use the Process revision evaluation action
to run an immutable draft or paused revision in an isolated Environment. The
evaluation can assert run completion, step outputs, approvals, trigger
behavior, retries, idempotency, and fixture side effects. A result is always
pinned to the exact revision that was tested.

## Safety

Assignments provide data and routing, not authority. Follow the frozen
procedure instructions and obtain required approvals before external actions.
Never place credentials in procedures, parameters, or run inputs; use
authorized connection references instead.
