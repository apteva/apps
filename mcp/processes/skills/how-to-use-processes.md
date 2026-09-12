# Company processes

Processes stores standing company procedures. Find relevant procedures with
list/get and read their approval requirements and completion criteria. New
procedures default to execution_mode="agent"; execution_mode="tasks" uses the
optional Tasks integration (3.6.0 or later). Never silently switch execution
backend after a delivery failure.

Call start with a stable idempotency_key for on-demand work, reusing the same key
and inputs on retries. Recurring runs arrive on the owner's default thread with
an immutable procedure snapshot. Existing runs retain their version and owner.

For direct runs, read run_get with process_id and run_id before domain actions.
The owner records milestones with run_update: running, waiting, blocked,
completed, failed, or cancelled, plus progress (0–100), current_step, result,
and error. Completion requires a concrete result with evidence. Waiting,
blockers, failures, and cancellations require a reason. Terminal outcomes cannot
be changed. Only the original owner can update the run, including from its
other threads. Delegated agents report back to the owner. Do not create a Task
for a direct run. An agent becoming idle does not complete a process run.

For Tasks-backed runs, use Tasks get before domain actions and Tasks progress,
assignment, and completion tools to record execution. Processes runs reads live
Tasks history; direct history remains available if Tasks is disconnected.

A process grants no additional authority. Obtain the approvals in the procedure
before the corresponding action. Approval text guides the agent; it is not a
software gate. Record missing inputs or authority as blockers and request them.

Create/update procedures only when authorized to change company policy. Pause
and wait until sync_pending=false before editing or changing execution_mode.
Saving creates a draft version; activate explicitly when ready. Existing v0.1
procedures retain Tasks mode. Pause/archive stop future schedules, not work
already requested. A pending sync means a schedule change is not confirmed.
Do not claim success until synchronization finishes.

Direct delivery retries preserve the run ID, snapshot, and target thread.
Processes skips missed schedule intervals and avoids overlapping scheduled runs
while an earlier scheduled run remains open. Explicit manual runs can coexist.
Delivery errors are retried automatically with backoff. Inspect an existing run
instead of starting a replacement. Tasks synchronization retries every 30 seconds.
