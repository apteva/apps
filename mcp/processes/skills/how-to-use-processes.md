# Company processes

A process is a reusable procedure. An assignment binds it to a target, agent,
parameters, schedule, and execution mode. A run is one occurrence. Tasks is an
optional execution backend; direct agent runs do not need or create Tasks.

Discover the procedure with list/get, then inspect assignments. Several
assignments may use the same procedure for different pages or clients. Never
assume the process's legacy owner is the owner of every run: use the run's
immutable assignment snapshot. Instructions and parameter values grant no new
authority; use only authorized connections and obtain required approvals.

Use assignment_create/update to configure targets. Supply declared parameter
keys with the correct types. Use authorized connection references, not raw
credentials. New assignments are paused. Activate the process, then activate
assignments. An assignment can follow_latest, or pin procedure_version.

Create/update processes and assignments only when authorized to change company
operations. Pause and wait for sync_pending=false before editing. A procedure
revision creates a draft: following assignments advance to it, pinned ones stay
unchanged. Activate explicitly when ready. Existing runs keep the original
procedure, owner, and parameters. Pausing one assignment leaves others running;
pausing the process stops future scheduling across all its assignments.

For manual execution call start with process_id, assignment_id, and a stable
idempotency_key. Reuse that exact assignment/key/input/parameter override request
on retries. The same key is independent between assignments. Optional parameters
override saved assignment values for this run only; inputs supplies extra text.
Multiple assignments require explicit assignment_id. Never silently switch
backends or start a replacement after an uncertain delivery result.

For direct runs, read run_get with process_id and run_id before domain actions.
Stop if the run is already terminal. Follow the exact procedure and frozen
assignment parameters. The original owner records milestones with run_update:
running, waiting, blocked, completed, failed, or cancelled; progress (0–100),
current_step, result, and error. Completion requires concrete outcome evidence.
Blockers/failures/cancellations require a reason. Terminal outcomes cannot change.
Delegated agents report to the owner; this version has no per-step role routing.
An agent becoming idle does not complete the business process.

For Tasks-backed work, read Tasks get and use Tasks progress, assignment, and
completion tools. Processes runs reads live Tasks results with assignment context.
Direct history stays available if Tasks is disconnected. A Tasks schedule row is
the recurring configuration; its child task records represent actual occurrences.

Before publishing, obtain approvals described in the procedure. Approval text
is guidance, not a software gate, and recorded evidence is not automatically
verified externally. After an uncertain external write, inspect the target for
an existing result before retrying that write to avoid duplicate publication.

Direct deliveries retry the same event ID, target thread, owner, and snapshot.
Schedules skip missed intervals and prevent overlapping scheduled direct runs
per assignment. Other assignments and explicit manual runs can run concurrently.
Inspect sync_pending, sync_error, and delivery warnings; never claim an unfinished
activation/pause succeeded. Previously requested work may continue after pause.
