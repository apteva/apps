---
name: how-to-use-tickets
description: Triage, communicate about, and resolve client feedback and support tickets.
---

# Tickets

Tickets owns the external request lifecycle. Keep client communication in public
comments and team-only reasoning in internal notes. Never expose an internal note
through a public comment.

When a new ticket arrives, read it with `tickets_get`, classify its area, type,
and priority, then acknowledge it with a public comment. Use `waiting_client`
only when a concrete response is required from the requester. Deadlines are
optional and should remain empty unless a real commitment exists.

Use Tasks for substantial internal execution and Code issues only after a ticket
has been triaged into repository-specific work. Link those records back to the
ticket; do not replace the client ticket with an internal record. Resolve the
ticket with a concise public explanation and reopen it when new client evidence
shows the original issue remains.
