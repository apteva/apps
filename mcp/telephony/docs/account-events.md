# Carrier account events

Telephony publishes two generic Apteva app-bus topics:

- `telephony.account.issue`: an observed carrier operation reported an account/service issue.
- `telephony.account.recovered`: sufficient later evidence resolved that particular issue.

These are notifications. They do not disable an integration, change call admission,
replace outbound eligibility checks, or perform balance polling. Existing
inventory-outage fallback behavior remains intact. No CRM changes are required to
produce events; consumers must subscribe to act on them.

Example `data` (the bus envelope also carries `event_id`, `topic`, and the
authenticated `project_id`):

```json
{
  "event_id": "unique-event-id",
  "issue_id": "stable-issue-id",
  "revision": 1,
  "provider": "telnyx",
  "carrier_connection_id": 108,
  "reason": "insufficient_credit",
  "severity": "error",
  "affected_operations": ["outbound_calls"],
  "message": "The carrier reports insufficient credit.",
  "source": "carrier_api",
  "occurred_at": "2026-10-25T12:00:00Z"
}
```

`call_id` is included when the evidence comes from an identified call. API
inventory/placement errors need not have a call yet. The provider and connection
come from platform metadata or the persisted call, never a browser-supplied
project or account name. Events contain fixed messages, not carrier response
bodies, phone numbers, credentials, or request parameters.

## Evidence and recovery

| Reason | Issue evidence | Recovery evidence |
| --- | --- | --- |
| `insufficient_credit` | Explicit recognized insufficient-funds/balance/credit reason in a bounded error object, authenticated callback, or final SIP rejection | Confirmed outbound answer for a call started after the last issue observation |
| `billing_blocked` | HTTP/SIP 402 or explicit recognized billing restriction | Same confirmed outbound-answer rule |
| `account_suspended` | Explicit recognized suspension reason | Same confirmed outbound-answer rule for outbound-call issues |
| `authentication_failed` | Carrier API HTTP 401 | Successful response to the same API tool on that connection/project |
| `rate_limited` | Carrier API HTTP 429 | Successful response to the same API tool on that connection/project |
| `provider_unavailable` | Carrier API HTTP 500/502/503/504 | Successful response to the same API tool on that connection/project |

The issue key includes project, connection, provider, exact operation scope, and
reason. Public operation categories are `outbound_calls`, `inventory`,
`call_control`, and `carrier_api`. Separate API tools may share a public category
but retain independent issue IDs and recovery evidence.

An HTTP 200 accepting a call is not proof that the call connected or billing is
healthy. Inventory success, inbound calls, and a delayed answer from an older
outbound attempt do not clear an outbound credit issue. A healthy SIP call does
not clear a REST authentication error. Recovery is conservative: billing or
suspension reported for operations other than outbound placement is retained
until specific supporting recovery evidence is implemented. No recovery is
inferred from elapsed time or binding removal.

Bare HTTP/SIP 403, busy/no-answer, ordinary SIP 401/407 digest challenges, opaque
transport errors, and arbitrary nested response text do not become credit
warnings. There are no guessed provider numeric-code mappings. Unknown error
formats remain ordinary call/API errors; support can be extended with explicit
provider fixtures. SIP setup/provisioning requests that bypass the common
carrier-tool executor are not monitored by this event source.

API observations use request-start ordering. Callbacks use normalized provider
event time. Answer recovery uses call placement time as a conservative watermark,
while the event occurrence time remains the answer time. This prevents older,
slower responses from reopening resolved issues or clearing newer restrictions.

## Delivery and cost

Migration 064 creates persistent issue state, observation watermarks, and an
outbox. A transition and its event are committed together. Repeated identical
failures update the observation watermark but do not emit reminders. Recurrence
after recovery increments the same issue's revision and produces a new event ID.

Existing project-scoped lifecycle workers and the event dispatcher deliver the
outbox; there is no new polling worker, carrier balance poll, or audio-frame
hook. Successful API observations require no additional gateway lookup. Classified
failures reuse a known provider from issue state, or make a bounded (500 ms)
authenticated metadata lookup. If provider metadata cannot be established, the
observation is logged and skipped; a later failure can retry it.
Observation writes have a 500 ms context budget and failures are logged without
changing the carrier operation's result. A failure to persist an observation
means that observation cannot be guaranteed delivery; a later observation can
retry the state transition.

Publication uses the generic app-event endpoint with installation authentication.
It is bounded to 20 events and two seconds per batch, with 500 ms HTTP requests,
exponential retry delay (1–256 seconds), and a 30-second lease. Events for an
issue are acknowledged in revision order. Missing gateway configuration leaves
events pending. Published outbox rows expire after 30 days in bounded batches;
pending rows and issue state are preserved.

Delivery is **at least once**: a gateway may accept an event even if its response
is lost. Subscribers must deduplicate `event_id`, and use `issue_id`/`revision`
to reject stale state. An HTTP acknowledgment confirms bus acceptance, not that
every subscriber processed the event. `recovered` refers only to the listed
operation/issue; it is not an account-wide health guarantee.

## Repeatable tests

From the Telephony directory:

```sh
GOWORK=off go test -run 'TestAccountEvent|TestLifecycleManifestDeclarationsMatchDisk|TestCarrierInventoryOutage|TestAccountFailure' -count=1 .
GOWORK=off go test -race -run 'TestAccountEvent|TestCarrierInventoryOutage|TestAccountFailure' -count=1 .
GOWORK=off go test ./...
```

The suite covers explicit classification, negative cases, redaction, concurrent
reports, account/project isolation, delayed observations, authenticated callback
rejection/acceptance, final SIP outcomes, recovery evidence, publication failure,
backoff, stable retry IDs, ordering, leases, timeouts, and carrier-result
preservation when event persistence fails. No live carrier calls are needed.
