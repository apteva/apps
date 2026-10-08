# Runtime number and carrier controls

Telephony runtime controls ship in 0.11.0. Full installation binding removal
also requires the separately implemented platform protocol described below.
No staging or production configuration has been changed.

## Independent controls

| Control | Scope | Existing calls | Other direction |
| --- | --- | --- | --- |
| Outbound number policy | One E.164 caller ID in a project | Continue, including media renewal and reattachment | Inbound route stays enabled |
| Outbound connection policy | All caller IDs on one carrier account in a project | Continue | Inbound routes stay enabled |
| Outbound provider policy | All bound accounts for a provider in a project | Continue | Inbound routes stay enabled |
| Inbound route enabled flag | One route | Continue with their pinned routing snapshot and callbacks | Outbound caller-ID ownership stays unchanged |
| Full carrier binding removal | One or more installation bindings | Drain before broker authorization is removed | Stops new admissions on the removed connection only |

These are admission controls, not access-policy edits. Existing adviser grants,
media tokens, codecs, buffers, pacers and transport selection are unchanged.
Number/provider controls are database-backed and do not use the platform
configuration endpoint which restarts sidecars. An in-flight placement settles
before a disable is acknowledged; later placements are rejected at the shared
human/AI carrier boundary. Idempotent retrieval of an existing call still works.
External adviser legs which continue an already admitted inbound call retain
that call's carrier context; they cannot bypass the guard without a durable
active parent/leg relationship.

## Operator and headless interfaces

All ordinary endpoints require existing project/operator authorization.
Delegated advisers cannot manage operational settings.

- `POST /numbers/outbound-policy` with `{}` lists disabled rules.
- To change a rule, send
  `{ "scope": "number", "value": "+33123456789", "enabled": false }`.
  Other scopes are `provider` (provider slug) and `connection` (connection ID
  encoded as a string). `enabled: true` removes that rule; other matching rules
  still apply. A number rule is validated against authorized carrier ownership.
- `POST /numbers/routes/disable` or `/numbers/routes/enable` with `route_id`
  changes new inbound admission immediately. Provider resources are retained.
  Re-enabling a draining/removed carrier requires rebinding it first.
- MCP: `telephony_outbound_policy` (platform principals only) and
  `telephony_routes_set_enabled(route_id, enabled)`. The existing
  `telephony_routes_disable` tool now retains external carrier resources.
- `POST /numbers/connected` returns administrative inventory, effective
  `outbound_enabled`, individual number-rule state, connection IDs, provider
  summaries and structured inventory warnings.
- `GET /softphone/numbers` returns only authorized, operationally enabled
  caller IDs. The public headless client exposes `outboundNumbers()`.
  Existing `/softphone/access` grants remain separate for live media.
- Cached dial attempts receive HTTP 403 with `code: "outbound_disabled"`.

A failing provider inventory or credential read no longer discards healthy
accounts, including other accounts on the same provider. `inventory_status`
is `available`, `partial` or `unavailable`; each
warning names the provider and connection. A completely unavailable inventory
is explicit, and the softphone choices endpoint returns HTTP 503. Failed
inventory does not authorize caller IDs or assert outbound readiness.

With the production SDK's cancellable integration client, accounts are queried
independently under an eight-second inventory budget (or an earlier request
deadline). Stalled accounts cannot occupy a shared worker pool and prevent
healthy accounts from starting. Completed inventories are preserved when the
budget expires; unfinished accounts receive `inventory_timeout` warnings.
Credential reads currently lack SDK cancellation: the inventory response stops
waiting for them at its deadline, and late completion cannot change that
response or start additional carrier reads. The underlying credential request
still uses the SDK's HTTP timeout. Legacy custom clients without cancellable
integration requests retain sequential compatibility behavior; they do not
provide the same deadline guarantee.

Successful inventory briefly prioritizes the owning account when dialing a
selected number. This avoids probing unrelated stalled accounts first. These
bounded, project-scoped hints never authorize a call: current binding,
credentials, ownership, readiness and admission checks remain authoritative.
Without a recent inventory hint, account discovery can still wait for earlier
accounts' bounded reads. Readiness and dial failures affect the selected
account only. Telephony does not substitute a different caller ID when a dial
fails. Calls without an explicit caller ID still use the configured default,
which can fail if that default account is unhealthy.

Disabled routes accept authenticated replay/callbacks for existing calls.
New XML-carrier arrivals receive terminal call control; Telnyx receives an
idempotent `reject_call` with explicit `CALL_REJECTED`. Direct SIP admission
remains disabled and a disable racing with setup receives a decline. These
rejected new sessions do not create call rows, adviser offers or missed calls.

The Numbers panel exposes separate inbound, number, connection and provider
controls, and keeps provider controls available when inventory fails.

## Full binding removal: generic platform protocol

This part requires the accompanying local **server change**, as the previous
platform unconditionally restarted apps on binding edits. No SDK change is
required. Other apps retain their existing behavior unless they explicitly
advertise authenticated `POST /_runtime/bindings` in their manifest.

The platform saves a durable change before invoking the app. Requests have an
app bearer token and HMAC-SHA256 body signature; the internal endpoint cannot
be invoked through ordinary adviser or operator requests. Each request has a
change ID, previous/desired bindings and `prepare` or `commit` phase.

1. Telephony prepares a durable drain for removed carrier connections and
   atomically stops their new admissions.
2. Desired additions/defaults become effective while removed connections remain
   in the effective bindings, retaining credential/tool/webhook authorization.
3. The app reports readiness only after live calls, media attachments, pending
   terminal effects, carrier activation, external legs and recording imports
   have drained. A 60-second terminal callback grace period also applies.
4. The platform atomically writes desired bindings with the durable commit
   phase, obtains an idempotent app acknowledgement, then cleans stale webhook
   registrations. The sidecar PID, endpoint and sockets are retained.

No timeout force-revokes a live connection. Pending or failed recording imports
can delay removal; resolve that work rather than bypassing the drain. Carrier
callbacks arriving beyond the terminal grace period after a completed removal
are no longer authorized. Provider-only historical recording access is not a
reason to keep a removed integration indefinitely.

HTTP PUT binding responses include `pending`, `desired_bindings`, effective
`bindings`, `change_id`, `retained_work`, `draining_connections` and
`respawned: false`. GET `/api/apps/installs/:id/bindings` exposes durable progress.
Overlapping edits return 409. Transient/unknown outcomes keep the durable change
and retained context; server startup resumes pending changes. Explicit
pre-mutation denials return an error without restarting. Telephony currently
opts into live changes for the carrier role; changes to other roles are refused
instead of restarting active calls.

## Local verification

- Full Telephony Go suite and frontend/audio tests.
- Focused Telephony and platform race tests.
- Two-way WebSocket audio, disable during an active call, and browser reattach.
- Cached dial denial, AI selection/final admission, idempotent call retrieval.
- Number/provider/account/project isolation and default carrier preservation.
- Incoming route pause/re-enable; existing ingress and status callback replay.
- XML/Telnyx rejection with no new adviser/missed-call projection.
- Partial and completely failed inventory, including credential-read failure.
- Stalled inventory and credential reads; four stalled accounts ahead of a
  healthy fifth account; separate and same-provider account isolation.
- Healthy simulated outbound placement after inventory/readiness/dial errors;
  stale inventory hints cannot grant ownership or retain an unbound account.
- Concurrent placement/disable ordering, external routing continuation.
- Pending recording/effect retention and durable drain state.
- Platform authorization retention, unchanged PID, overlap rejection,
  transient prepare failure, explicit refusal and lost commit acknowledgement.
- Telephony panel builds and host import verification; headless type checking.

Tests use local protocol peers and test databases. No real carrier calls or
production installation were used. Deployment of both matching changes is
required before claiming full binding removal works on an installed instance.
