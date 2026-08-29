# Using Tasks

Tasks are durable work records. Threads are opaque identifiers: never infer a
platform role from a thread ID.

Task inventory is agent-wide within the current project. A task created,
assigned, or executed by any thread of the agent remains visible from every
other thread of that agent. Use `tasks_list` directly for task or schedule
questions. Never narrow the inventory by the current thread and never ask
another thread merely to list tasks. Creator, assignee, and executor thread IDs
are provenance and routing metadata, not visibility boundaries.

## When to create a task

Create one task before substantive work begins when the requested outcome is
multi-step, combines multiple sources or independent checks, is delegated,
scheduled, or must continue after the current exchange. A review that inspects
several areas and synthesizes them is task work even when its calls can run in
parallel or the result can be finished in the current turn.

Do not create a task for a greeting, a brief answer, or one bounded lookup or
action that needs no multi-source synthesis. Finishing quickly does not by
itself make multi-step work a quick lookup. Creating a task also does not imply
delegation: the calling thread may create, execute, update, and complete its own
task.

One user outcome is one logical task. Adding a schedule or assigning the task
must update that task; do not create a second “setup” task. Recurring executions
are bounded occurrence records created by Tasks and are not additional user
requests.

## Ownership and threads

The calling thread is always the creator. Immediate work defaults to that
creator. Scheduled work defaults to the agent's configured default thread so a
schedule does not depend on the requesting thread staying active. Assign an
explicit opaque thread only when another existing thread should own the work.
Tasks records work but does not create threads. When separate context,
ownership, or parallel execution is useful, main creates the worker with the
platform `spawn` tool and grants `tasks_get` plus only the domain tools it
needs. Do not grant delegated workers `tasks_create`, `tasks_update`,
`tasks_assign`, `tasks_complete`, or other Tasks mutation tools. Main remains
the ledger writer: the worker reports meaningful milestones and its final
result to its parent, and main records them with `tasks_update` and
`tasks_complete`.

Prefer a paused worker with a general task-execution directive, then call
`tasks_assign` with that existing opaque thread ID. The committed assignment
event supplies the authoritative task ID and wakes the worker only after the
durable assignment is recorded. Before any domain action, the worker must call
`tasks_get` with that ID and treat the returned task record as authoritative.
It must preserve exact strings and must not substitute parent context for the
record. If `tasks_get` fails or any execution-critical value is absent or
unresolved, the worker stops before making external changes and reports the
specific missing fields to main. Phrases such as “use the copy from the task”,
“as discussed above”, or “use the supplied media” are not concrete execution
values.

Main may read the task before spawning to select the necessary domain tools,
but it should not paraphrase the task into a second source of truth. The task
ID is traceability, not a request for the worker to infer content: the worker
resolves it through `tasks_get`. A non-main thread that needs delegation asks
main to perform this orchestration. These are task relationships, not platform
thread roles: the task record defines who owns the work.

When a task event reaches any thread, including a delegated worker, read the
task with `tasks_get` before acting. A terminal receipt sent to the creator is
context for that thread; the thread decides whether a user-facing message,
report, alert, or no publication is appropriate.

## Progress

Progress is task-level, not thread-level. Keep a task `running` while any
executor is actively working, including a delegated worker. Use `waiting` only
when nobody can currently advance the task because it awaits an external event
such as user approval, an API callback, or a retry window. When that event
arrives, return the task to `running` with an updated concrete `current_step`.

Use `tasks_update` at meaningful phase boundaries, waits, blockers, and
failures—not after every tool call. A short or empty check may move from its
first running update directly to completion. Multi-stage, multi-message, or
delegated work should normally record at least two or three intermediate
milestones so the task does not sit at its initial percentage throughout the
work. Percentages represent completed work and remain monotonic; changing
threads or entering `waiting` does not by itself increase progress.

`tasks_update` also edits an existing task definition. Call `tasks_get` first,
then send the changed `title`, `description`, and/or partial `schedule` together
in one update; omitted fields are preserved atomically. For a recurring task,
update the recurring parent ID rather than an occurrence ID. Editing its
schedule preserves whether it is paused, and future occurrences inherit the
new title and description. Occurrences already materialized remain immutable
snapshots. Never create a placeholder task to rename or reschedule an existing
one.

For example, a four-message delegated inbox review could record:

- `running` · 10% · `Listing unread Gmail messages`
- `running` · 25% · `Found 4 unread messages`
- `running` · 40% · `Gmail review worker processing 4 messages`
- `running` · 70% · `4 messages classified and marked read`
- `running` · 85% · `Publishing attention alert for Clima Confort`
- `completed` · 100% · result `Processed 4 messages; 1 alert published`

Do not mirror task progress into global agent status. Finish exactly once with
`tasks_complete`, which records 100% and `Completed`, or use failed/cancelled
when that is the real outcome.

### The terminal write is mandatory

`progress: 100` is not a terminal state, and a final-looking `current_step`
does not complete a task. Before the thread calls `pace`, returns idle, stops,
or sends its final response, inspect every task it advanced during that turn.
If the outcome succeeded, it must call `tasks_complete` exactly once with the
concrete result. If the outcome failed, it must record `failed` with a concrete
error. Leave a task `running`, `waiting`, or `blocked` only when work genuinely
remains or an external dependency is still outstanding.

The same rule applies to delegation. A worker's final message is not a Tasks
terminal write. As soon as the worker reports a successful result, main must
record the remaining meaningful milestone and immediately call
`tasks_complete` before main paces, idles, stops, or answers the user. Core
lifecycle settlement is only a safety net for an omitted terminal write; it
does not replace this MCP obligation.

## Scheduling

Use `once` with an RFC3339 `at` timestamp or a relative `after` duration. Use
`interval` or five-field `cron` for recurrence. Server time is authoritative.
List, pause, resume, cancel, and run schedules through Tasks directly; do not
ask another thread merely to inspect the schedule inventory.

Every due schedule has an occurrence lifecycle. Scheduler dispatch is not
workflow success. On platforms with tracked agent events, Tasks records
acceptance automatically when Core claims the occurrence and records execution
activity separately from business progress. The assigned thread must still
read the authoritative record with `tasks_get` before any domain action; on
older platforms that read also accepts the occurrence. The agent—not Core—then
records meaningful running milestones and exactly one terminal outcome through
the Tasks MCP tools. Always include a durable `result_reference` when the work
creates an output ID or URL.

When Core reports that the complete causal execution, including its workers,
has settled, Tasks does not infer success. It allows a short terminal grace
period. If the agent omitted completion or failure, Tasks fails the occurrence
with `agent_exited_without_terminal_status` so an abandoned run cannot block
the next schedule. A dispatched occurrence that is never accepted is failed
automatically; do not recreate it merely because the parent schedule is still
active. The recurring parent reports scheduler state separately from its latest
occurrence status and error.
