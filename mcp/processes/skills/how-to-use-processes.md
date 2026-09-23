# Processes

Processes defines immutable procedure revisions, assignments, schedules, event
triggers, native runs, and durable step-run history. A run is one occurrence of
an assignment. Every step is tracked by Processes with its own state, progress,
output, evidence, retry metadata, and revision.

## Execution

Define procedures semantically: step keys, instructions, roles, outputs, and
dependencies. Never supply graph coordinates; Processes lays out every graph
automatically. Use `validate_definition` for a read-only readiness check, then
`create` to save an unassigned draft. If the user names an executor, use
`assignment_create` after creation; the new assignment is paused. Activate the
reviewed procedure, activate the assignment, and start a run with a stable
`idempotency_key` only when the user explicitly authorizes each deployment
action.

Text in `approval_requirements` is frozen procedure policy. Steps are generic:
when approval is required, put that requirement in the relevant step instructions
and use the appropriate communication or integration tool to obtain it.

Before acting, read `run_get` or `step_get`. Agents report meaningful progress
and the terminal outcome with `run_update` or `step_update`. Completion requires
concrete output and evidence.

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
evaluation can assert run completion, step outputs, required external approvals, trigger
behavior, retries, idempotency, and fixture side effects. A result is always
pinned to the exact revision that was tested.

## Safety

Assignments provide data and routing, not authority. Follow the frozen
procedure instructions and obtain required approvals before external actions.
Never place credentials in procedures, parameters, or run inputs; use
authorized connection references instead.
