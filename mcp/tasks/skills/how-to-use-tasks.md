# Using Tasks

Tasks are durable work records. Threads are opaque identifiers: never infer a
role such as “main”, “conversation”, or “worker” from a thread ID.

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
schedule does not depend on a UI conversation staying active. Assign an
explicit opaque thread only when another existing thread should own the work.
Tasks records work but does not create threads. When separate context,
ownership, or parallel execution is useful, main creates the worker with the
platform `spawn` tool and grants only the domain tools it needs. Keep Tasks
ledger tools with main: the worker reports meaningful milestones and its final
result to its parent, and main records them with `tasks_update` and
`tasks_complete`. Prefer a paused worker, then call `tasks_assign` with that
existing opaque thread ID so the durable assignment is recorded before work
starts. The committed assignment event wakes the worker with the task context;
send additional context explicitly if needed. A non-main thread that needs
delegation asks main to perform this orchestration. These are task
relationships, not platform thread roles: the task record defines who owns the
work.

When a task event reaches a thread, read the task with `tasks_get` before
acting. A terminal receipt sent to the creator is context for that thread; the
thread decides whether a user-facing message, report, alert, or no publication
is appropriate.

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

## Scheduling

Use `once` with an RFC3339 `at` timestamp or a relative `after` duration. Use
`interval` or five-field `cron` for recurrence. Server time is authoritative.
List, pause, resume, cancel, and run schedules through Tasks directly; do not
ask another thread merely to inspect the schedule inventory.
