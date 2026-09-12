# Company processes

Use Processes for standing company procedures, and Tasks for actual execution.
Find a relevant process with list/get. Read the procedure, approval requirements,
and completion criteria before starting. For ad hoc execution call start with a
stable idempotency_key; it returns the authoritative Tasks task. Scheduled runs
arrive directly as task events with a fixed procedure snapshot. Always call
Tasks get on a received task before taking domain actions. Record milestones,
blockers, assignment, and final evidence using Tasks, not Processes.

The assigned owner agent receives work on its configured default thread. Worker
delegation follows the Tasks contract. A process does not grant new permissions.
Follow explicit approval checkpoints; do not mark a task complete before its
required evidence and approvals exist. Approval text is guidance, not a software
gate. If inputs or authority are missing, record a blocker in Tasks and request
what is needed.

Create/update company procedures only when authorized to change company policy.
Pause an active procedure and wait for synchronization before editing. Update
creates a new immutable version in draft; activate explicitly when ready. Existing
runs keep their original snapshot. Pause/archive stop future schedules; they do
not cancel already queued or running tasks. Required inputs can identify sources
the owner must collect. Supply run-specific context through start inputs, or
standing context in the procedure's default_inputs.

A pending sync/error means the requested lifecycle change is saved but Tasks has
not confirmed it. Do not claim activation or pausing finished until sync_pending
is false. Retry start with the same key after transport failure to avoid duplicate
work. Automatic retries reconcile Tasks every 30 seconds. A delivery warning
means the task exists but its event could not yet be delivered; inspect it rather
than starting a replacement.
