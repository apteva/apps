# Processes 0.18.5

Restored-worker repair now reads thread-start metadata for one known cleanup
owner at a time, in 25-event pages with independent durable cursors. This avoids
the platform query deadline encountered by v0.18.4's project-wide history read
on the busiest production project. Stopped owners are deferred without waking
them; they cannot block observation of other agents.

Migration 028 initializes agent-specific scan targets from existing cleanup
ownership. Subsequent cleanup acknowledgements register new owners transactionally.
The v0.18.4 batch isolation, per-wait retry backoff and stable tracked event IDs
remain in place. Only restored exact terminal-run workers can reopen old cleanup
receipts; unfinished workflows, real approvals, callbacks, native timer owners,
newer acknowledgements and pending retry order remain protected.

Validation: complete Go unit/integration suites, focused batch/restore/timer race
checks, Go vet and Linux amd64 build. The same unchanged UI passed 206 Bun and
44 Playwright tests; all three existing live Core tier 3 checks passed during this
release session. The narrowed 25-event metadata request was also checked against
the existing production API before deployment. No Core/Server source or binary
changes, new projects or synthetic production workflows are required.
