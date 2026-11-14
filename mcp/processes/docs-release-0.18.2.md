# Processes 0.18.2

Processes releases non-delegating workers while their workflow waits for a saved
future deadline or an explicit recoverable runtime hold. Continuations preserve
step outputs, evidence and deadlines and receive a fresh worker and delivery
identity. Retirement is recorded before a worker is told to finish, and cleanup
retries retain their persisted order.

Business waits keep their original approval or callback owner, including after
a later provider error. Workers configured to delegate retain ownership across
waits so their child callbacks remain valid. Existing platform APIs are used;
this release requires no Core or Server changes.

A shared `max_workers_per_agent` setting (default 8) limits Processes root workers
across assignments and runs in this installation. Excess work stays queued until
a worker slot is released. Delegated Core children are outside this app limit.

Runtime lifecycle errors immediately refresh the parent run's saved status.
Provider retry exhaustion blocks and suspends the run without another model
request. Saved external-operation receipts remain available for explicit recovery.

Migration 025 adds durable worker admission, retirement reasons and callback
ownership, including a backfill from existing step history. SDK remains pinned
to v0.115.1. The recommended email-routing setup is a one-step routing procedure
that records a verified durable handling-run ID and then finishes; customer
handling and approvals continue independently.
