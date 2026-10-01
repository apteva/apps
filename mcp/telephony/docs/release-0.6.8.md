# Telephony 0.6.8

This release removes the project-wide call-history scan from application-user
call lists and stream snapshots.

- `/user/calls` now gathers call candidates through indexed ownership, active
  browser offers, direct pending destinations, and supervisor resources. The
  existing per-call permission checks still decide visibility and answerability.
- `/user/calls?call_id=...` reads the requested call directly before any list
  work, while preserving the same project and user visibility checks.
- The headless client reports failed list-request status and elapsed time. The
  Telephony panel shows these diagnostics in its call-refresh warning.
- Recovery polling and the existing SSE change hints remain in place. A later
  release can deliver adviser-scoped call IDs without relying on a project
  history scan.

Regression tests cover a 3,641-call project, direct and offered inbound calls,
owned history, supervisor visibility, exact-ID permission checks, indexed query
plans, and failed browser refresh diagnostics.
