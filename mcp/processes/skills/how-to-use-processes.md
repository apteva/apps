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

## Worker thread continuity

Set assignment `worker_continuity` to `per_executor` to retain one worker thread
per run and executor agent across branches, dependency joins, delays, and human
gates. This serializes ready steps on each executor. `auto` retains the existing
strict untimed chain optimization; `isolated` uses separate step workers for
parallelism. The choice is frozen in each run. Preserve dependency records and
exact output receipts rather than replacing them with remembered context.
Workers use `step_claim` before acting and inspect `step_update.done`: call the
native done tool immediately when true; otherwise follow `next_action`: continue
the current step after progress, or await the next app event after completion.
Reuse prepared state and tools; do not spawn another worker, forward steps,
repeat completed actions, or send per-step reports to main. Human approval can
only be completed by the authorized project operator.

## Compact worker responses

`step_get` and `step_claim` return the assigned step and its full saved checkpoint,
one authoritative `dependencies` manifest with ancestor IDs, states, direct flags
and exact output receipts, plus shared frozen policy and resolved inputs. They
omit unrelated procedure steps and delivery diagnostics. After retaining the
shared policy, pass `include_context=false` on subsequent reads/claims to omit
that policy, inputs, parameters and assignment details. `context_ref` identifies
the frozen procedure/assignment revisions. Omit the flag or set it to true to
recover context after a restart or lost reply. Never omit policy you no longer
remember; durable receipts remain authoritative even in a reused worker.

`step_update` returns an acknowledgement with IDs, accepted revision/state/progress,
run state, `done`, `next_action`, and a blocker reason when present. It does not
echo instructions or receipts. Use `step_get` to recover saved output and
`run_get` for explicit full inspection. Retrying an accepted completion with
the same evidence does not repeat writes or downstream dispatch.

## Discovery and full-state recovery

MCP `list` returns discovery metadata and a `reread` reference for each procedure,
not execution instructions. MCP `get` returns current content once with historical
version metadata. To retrieve a specific immutable revision, follow that version's
reference or supply `version`; the result contains the selected `definition` and
procedure metadata, without also returning another revision's instructions.

MCP `run_get` returns the immutable definition once and exact run/step states and
evidence, including human approval receipts and attribution. It omits the duplicate
text snapshot and repeated step definitions; step references recover assigned
frozen instructions. MCP `run_update` returns IDs, frozen procedure version, saved
state/progress, `done`, a blocker reason when present, and a `reread` reference.
Exact results remain stored and recoverable with `run_get`. Step reads and updates
also include a reference to `processes_step_get` with full shared context enabled.

A reference has `{tool, args}` naming an existing Processes tool and exact arguments.
Use it when recovery or verification is needed; do not poll or replace saved receipts
with memory. HTTP/UI responses and stored procedure definitions are unchanged.

For useful parallel execution across independent steps of one executor, configure
`worker_continuity="per_executor"`, `parallel_execution="auto"`, and optionally
`max_parallel_steps` (1–8, default 4). These policies are frozen into the run.
The persistent worker decides the Core subthread arrangement. Claim every eligible
step before domain action or delegation; use `ready_steps`/`active_steps` hints to
find this owner's work, then read/claim exact IDs. Never bypass dependency or time
gates. The concurrency limit includes running, waiting and blocked claims.

Retain shared setup, keep thread-bound sessions and shared mutable operations
sequential, and delegate only transferable independent work. Children return exact
results to the owner and must not claim/update Processes or self-approve. The owner
checks and saves separate exact receipts and checkpoints child/operation identities
before waiting. Recover by reading saved checkpoints and inspecting existing Core
children before retrying; do not replace outstanding work. Keep the owner alive
until all its steps and delegated work settle, respecting the final validation and
human approval gates. Existing assignments remain sequential unless opted in.
