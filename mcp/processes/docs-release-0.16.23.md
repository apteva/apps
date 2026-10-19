# Processes 0.16.23

A tracked workflow execution ending in event.error now records the runtime reason and blocks that step with delivery suspended after Core exhausts the request retry budget. Stale-step reminders cannot repeatedly wake the same failed context. Explicit reconciliation is required before recovery: runtime failure does not prove whether external publishing succeeded.

Existing output, progress, accepted operation receipts, and completed business steps remain intact. Later transport settlement does not remove the hold. No Core binary, Social app, scheduling, or unrelated process authority changes.

Validation: full Go suite, including runtime-error hold/no-reminder, later settlement, completed-step preservation, and existing lifecycle source/project checks. SDK remains v0.102.0 (newest topology descendant).
