# Generic routing decisions (Telephony 0.5.0)

Telephony owns offers, capacity, answering and audio. A bound Functions app owns
business selection, such as availability, quotas, customer priority and repeat
caller rules. No consuming app or function name is hard-coded.

## Configure

Use Telephony → Routing → Advanced flow editor, or the existing operator routing
HTTP APIs and `telephony_destinations_create`, `telephony_flows_create`,
`telephony_flows_publish`, and `telephony_flows_assign_numbers` MCP tools. Ordinary
application-user credentials cannot administer routing. Bind/install Functions in
the same project; its dependency is optional for flows without decisions.

Create an individual browser destination with this `config` (replace identity
values with the actual verified issuer identity):

```json
{
  "capacity": {
    "identity": {
      "issuer_app": "auth", "issuer_install_id": "209",
      "subject_type": "user", "subject_id": "40", "organization_id": "1"
    },
    "concurrent_call_limit": 1
  }
}
```

Grant that identity access to the destination using `/access/policy`, as documented
in [application-users.md](application-users.md). Publishing checks the assignment
and grant. Runtime selection checks them again. Revocation continues to be enforced
by the existing authenticated answer/media APIs.

Example flow draft, with a saved static overflow group:

```json
{
  "entry": "choose",
  "nodes": [
    {
      "id": "choose", "type": "decision",
      "config": {
        "function_id": 42,
        "timeout_ms": 2000,
        "ring_timeout_seconds": 20,
        "destination_ids": ["dest_alice", "dest_bob"],
        "variables": {"service": "sales"}
      },
      "branches": {"fallback": "overflow"}
    },
    {
      "id": "overflow", "type": "ring_group",
      "config": {"ring_group_id": "group_reception"}
    }
  ]
}
```

Publish and assign the flow to a Twilio or Telnyx webhook number. Direct SIP does
not support decisions. The published version pins the function ID, configuration
and permitted destination snapshots. **Functions executes the active function
version**; it does not pin function source. Deploy business-rule changes with that
policy in mind.

The destination allowlist contains individual browser destinations only. Shared
pools keep their existing behavior and can be used as static fallback groups.
The decision cannot return arbitrary numbers, ring groups or URLs.

## Function contract

Telephony calls `functions_invoke` with `id` and this `event`:

```json
{
  "schema_version": 1,
  "decision_id": "decision_call123_choose", "attempt": 1,
  "project_id": "project123", "call_id": "call123",
  "flow_version_id": "flow_v1", "node_id": "choose",
  "caller": "+33600000000", "called": "+33100000000",
  "digits": {"menu": "1"}, "variables": {"service": "sales"},
  "previous_decisions": [], "previous_offers": [],
  "deadline_at": "2026-09-13T12:00:02.000000000Z"
}
```

Return an object as the function result (Functions serializes it in its response):

```json
{
  "decision_id": "decision_call123_choose", "action": "offer",
  "destination_id": "dest_alice", "reservation_id": "reservation456",
  "ring_timeout_seconds": 20
}
```

Or return `{"decision_id":"decision_call123_choose","action":"fallback"}`.
Echo the exact decision ID. The optional reservation ID is an opaque correlation
string, at most 256 characters. Ring time defaults to the configured value and
must be between 5 seconds and that configured maximum (at most 60 seconds).
Unknown fields, malformed responses, unlisted destinations and unavailable
capacity follow the saved fallback. The decoded response is limited to 16 KiB.
Only explicitly configured `variables` are sent; no CRM data is fetched implicitly.

Use `decision_id` as the business reservation idempotency key. Respect
`deadline_at` and give reservations an expiry. The deadline includes scheduling
and dependency time, defaults to 2 seconds, and is capped at 5 seconds. Work is
checked every second; an expired result is never accepted. A timed-out/canceled
platform invocation may still finish inside Functions because its SDK transport
does not support per-invocation cancellation. Telephony ignores its result and
caps outstanding invocations at 32. Do not rely on cancellation to undo business
side effects; use TTLs and reconcile outcomes.

## Offers, capacity and recovery

A result, capacity reservation and one-member ring offer commit atomically.
Duplicate provider callbacks never create another independent decision. A restart
expires uncertain running work at its original deadline instead of invoking it
again. Committed offers survive restarts. Carrier delivery uses existing
idempotent commands. Calls that end or advance reject late results.

Capacity is installation/project-local and keyed by verified identity, not
address. Aliases share the strictest configured limit (1–10 calls), including
outbound calls owned by that identity. Destination reservations also cover trusted
operator answers; shared destinations do not gain an implicit per-person limit.
Failed setup, canceled/expired offers and completed calls release reservations.
Connected calls retain occupancy through temporary audio disconnections.
Changing an identity assignment invalidates a pending decision's pinned target.

No answer or failed setup follows `branches.fallback`. For reselection, point it
to a second decision node with its own fallback. The request then includes previous
decisions and offer outcomes. Flows are acyclic and allow at most four decision
nodes; always end with a static fallback. Presence/heartbeat, durable queues,
external webhooks and new voicemail support are outside this release.

## Outcomes and diagnostics

`GET /routing/decisions?call_id=...&project_id=...` and
`telephony_decisions_list({call_id})` expose project-scoped decision requests,
results, status, reason, deadline and duration. The panel has a call-ID trace
viewer. Existing `telephony_call_events_list` provides durable event reconciliation.

New topics start with `telephony.routing.`:

- `requested`, `accepted`, `rejected`, `timed_out`, `fallback`, `canceled`.
- `offer.offered`, `offer.claimed`, `offer.answerer`, `offer.failed`,
  `offer.expired`, `offer.canceled`.
- `destination.connected`, `destination.connection_failed`.
- `call.completed`, `call.failed`, `call.busy`, `call.no-answer`, `call.canceled`.

Each includes an event ID, revision, time, call/decision IDs, reservation ID and
selected destination where applicable. Offer events include the offer ID.
`offer.claimed` is not proof of audio. `offer.answerer` records the verified user
once ownership is attached; `destination.connected` records actual media
connection and the verified answering identity when available. Trusted operator
credentials have no application-user identity and are not mislabeled as the
configured destination user. Events are journaled transactionally, then delivered
through the existing retryable outbox. Deduplicate by `event_id`; use revisions
and the read APIs to reconcile after interrupted delivery. Do not settle business
quotas from an offer claim alone.

Simulation accepts `context.decisions`, keyed by node ID, for example
`{"choose":{"action":"offer","destination_id":"dest_alice"}}`. Missing
mock decisions take their fallback. Simulation never invokes Functions or creates
business reservations. Browser microphone processing, playback, codecs and the
shared `createSoftphone()` API are unchanged.
