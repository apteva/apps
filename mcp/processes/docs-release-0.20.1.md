# Processes 0.20.1

Steps show that they are being worked on again.

In 0.20.0 an isolated step worker reports only its outcome, so a step stayed
"ready" until it finished: the panel said "Ready to start" for the whole run and
the step history went straight from ready to completed. In 0.19.4 the worker's
own `step_update(running)` call filled that gap.

Processes now marks the step running itself as soon as its worker has been
started and the step delivered, and records a "Worker started" entry (actor
`workflow`) in the step history. This costs no model call. A worker that
finishes before delivery returns keeps its completed state: only a step that is
still ready moves to running.

Final states, outputs and receipts are unchanged. Persistent workers already
moved to running on `step_claim` and are not affected.

Validated with `GOWORK=off go test -race ./...` (one new test), 209 Bun tests and
the TypeScript check. No UI, API, migration, Core, Server or SDK changes.
