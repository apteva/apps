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
Use `tasks_spawn_thread` only when separate context, ownership, waiting, or
parallel execution is useful. These are task relationships, not platform thread
roles: the task record defines who owns the work.

When a task event reaches a thread, read the task with `tasks_get` before
acting. A terminal receipt sent to the creator is context for that thread; the
thread decides whether a user-facing message, report, alert, or no publication
is appropriate.

## Progress

Use `tasks_update` at meaningful milestones, waits, blockers, and failures.
Keep `current_step` concrete and use coarse progress percentages only when they
help. Do not mirror task progress into global agent status. Finish exactly once
with `tasks_complete`, or use failed/cancelled when that is the real outcome.

## Scheduling

Use `once` with an RFC3339 `at` timestamp or a relative `after` duration. Use
`interval` or five-field `cron` for recurrence. Server time is authoritative.
List, pause, resume, cancel, and run schedules through Tasks directly; do not
ask another thread merely to inspect the schedule inventory.
