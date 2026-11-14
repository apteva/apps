# Processes 0.18.3

Processes separates durable waits from live worker ownership. `step_update.wait`
records a recoverable hold, a Processes-owned timer, or a real thread-bound
approval/callback/native timer. A checkpointed hold automatically suspends its
step and releases its worker without replaying external operations. Processes
resumes its own timer waits at the saved exact deadline, from preserved output,
after cleanup acknowledgement and with a fresh worker/delivery identity.

Real approvals, callbacks and already armed native timers retain their owners.
They are never cancelled, recreated or expired just because they look old.
Legacy waits receive a bounded, idempotent ownership-revalidation event after
ten minutes. Their worker verifies original dependency records before declaring
a stale hold; unknown ownership or a missing response fails closed. Delegating
workers remain attached. This release deliberately does not infer callback
completion from free-text blockers or implement a blanket idle timeout.

Migration 026 saves wait ownership, indexed timer deadlines and acknowledged
revalidation revisions. The existing event/deadline dispatcher and cleanup retry
order are retained. No new permissions, app dependencies, Core/Server changes,
production projects, or production deployment are included. SDK is pinned to
v0.115.2, the latest tag by ancestry at release time.

Validation: full Go unit and integration suites; focused lifecycle race tests;
Go vet; Linux amd64 build; 206 Bun unit tests; 44 Playwright browser tests. The
new HTTP/SQLite sidecar test released its waiting worker before restart and
resumed the exact checkpoint timer 13 ms after its deadline. Regressions cover
lost cleanup acknowledgements, a crash before retirement reservation, stable
revalidation retries, explicit recovery, and preserved approval/callback/native
clock owners across provider failures. Existing delayed-step, trigger, dispatch
and cleanup-order tests remain passing.

The three existing live Core tier 3 checks passed without source changes: child
creation/inference continuity, inherited tool discovery, and exact tool scope.
The child-creation check needed retries after its intermittent missing-raw-model-
JSON assertion; that failure was not hidden or changed in Core.
