# Telephony on a managed execution backend

Telephony 0.12.14 implements the app lifecycle required by Apteva's locally
implemented `apteva.runtime-backend/v1` controller. Placement is optional. A
normal local installation continues to start and serve normally. This change
adds no Instances or Domains integration dependency and does not deploy an
execution backend.

## Ownership and replacement

The app retains `activation_strategy: exclusive`, writable SQLite and one active
execution. It declares `passive_start: true`, `drain: true`, `replica_safe: false`
and `worker_coordination: false` at protected
`GET /_apteva/runtime/app/describe`. Multiple active replicas and live-call
migration are unsupported.

A managed process requires all of:

- `APTEVA_RUNTIME_MODE=standby`;
- `APTEVA_DEPLOYMENT_ID` and positive `APTEVA_DEPLOYMENT_GENERATION`;
- a reachable HTTPS gateway (loopback HTTP is allowed for local execution);
- installation-specific incoming/outgoing process credentials supplied by Server.

On mount, standby verifies identity, restores no call ownership, does not reset
media claims/imports and does not run routing, recovery or other business workers.
It validates retained recordings without rewriting their references. SIP binds
its required listener for readiness, but new INVITEs are not admitted. The SDK
opens/migrates the app database before `OnMount`: candidates must use isolated
state, or start only after the previous execution has been confirmed stopped.
This app cannot make concurrent SQLite migrations safe.

`POST /_apteva/runtime/app/activate` accepts `{deployment_id,generation}` and
acknowledges **armed**. Server calls it before committing ownership. The app
opens admission only after its authenticated
`GET /api/apps/callback/runtime/ownership` reports the same active identity and
a valid expiry. Local initialization is tracked while ownership renews; a drain
or loss of ownership during initialization cannot reopen admission.

Ownership checks run in the background, once per second, with bounded HTTP calls.
Transient errors retain the last verified lease. Expiry, explicit denial or an
ownership change fence the execution. Media sockets are canceled/closed and SIP
is stopped. A fenced execution requires restart; a later callback cannot revive
it. None of these callbacks runs per audio frame.

`POST /_apteva/runtime/app/drain` closes new-call and configuration admission.
Existing answers, media, renewals, hangups and provider callbacks continue while
ownership remains valid. Inbound admission is checked at **call creation**:
Telnyx's shared inbound event route must still accept events for existing calls.
The response reports active calls, recording imports, recording writers and
tracked HTTP, worker, SIP, routing, AI handoff/preparation work. It reports
`drained: true` only when these have finished and further work is gated. SSE
streams have the existing 20-second lifetime, so idle subscriptions can briefly
delay drain. Stop long-lived diagnostic echo probes before requesting a move;
their tracked connections also prevent a completed drain. Server must confirm
process termination before snapshot transfer.

A normally started local process can be adopted and drained through the same
protected API after Server's ownership callback identifies it. Local generation
zero is valid for adoption. It can resume after a canceled cutover. This does
not bypass Server authorization or fix the Server rollback prerequisite below.

## Public endpoints

The backend provisions networking and supplies secure **base URLs**, without the
Telephony installation prefix, in `APTEVA_RUNTIME_ENDPOINTS`:

```json
{
  "browser": "https://voice.example.com",
  "media": "https://voice.example.com",
  "signaling": "https://voice.example.com",
  "callbacks": "https://voice.example.com",
  "carrier_media": "wss://voice.example.com"
}
```

The named keys are Telephony's conventions over Server's generic string map:

| Name | Use |
| --- | --- |
| `browser` | Scoped human-call HTTP, notifications and session authorization |
| `media` | Browser PCM/WebSocket audio ingress; defaults to `browser` |
| `signaling` | Browser WebRTC control ingress; defaults to `browser` |
| `callbacks` | New carrier webhook/call-control URLs; absence retains Apteva proxy URLs |
| `carrier_media` | Carrier media WebSocket URLs; absence retains Apteva proxy URLs |

All browser bases must route `/runtime-browser/*` to this same execution. The
backend must also return a `browser_endpoint` in its execution descriptor for
Server credential issuance; the current controller does not automatically copy
that field into the named `browser` environment entry. Supply both. Private
`/_apteva/runtime/*` control ingress must remain private and authenticated.
URLs with credentials, query strings, fragments, insecure non-loopback schemes
or ambiguous traversal are rejected at startup. Original call/media tokens
remain mandatory. Endpoint discovery never includes a process credential.

SIP and WebRTC UDP media still need explicit execution-host networking:
`sip_public_host`, `sip_public_ip`, signaling listener, RTP ranges, certificate
paths, and `softphone_webrtc_public_ips`/UDP range. Managed SIP rejects
implicit use of the management gateway's host. The backend must allocate and
open these ports and provision usable certificates. The generic Server contract
currently supplies named HTTPS/WSS endpoints, not a typed raw SIP/RTP/certificate
binding. Telephony does not provision DNS, hosts or firewall rules itself.

Existing provider callbacks using installation-scoped Apteva URLs continue to
work through Server's proxy to the selected execution. An endpoint advertisement
does not automatically reprovision already configured carrier resources.

## Browser transport

The app-owned client discovers `/runtime-endpoints` through its existing
installation handle. Default `runtime: "auto"` retains the proxy on legacy
servers and uses direct browser endpoints when a managed execution advertises
them. `runtime: "proxy"` explicitly keeps the proxy; `"direct"` requires a
direct endpoint. Node/non-browser clients retain proxy behavior in auto mode.

The authenticated proxy bootstrap exchanges the original **user** credentials
with Server's five-minute browser-credential API. The direct API uses only that
scoped bearer credential; host credentials and cookies are not forwarded to the
backend. Direct media retains the existing call-specific token, bound to the
verified browser scope from session creation/renewal. Direct operator place/answer
responses also receive the standard renewable media lease; the browser credential
can refresh without a five-minute audio disconnection. Browser scope is checked
on attachment and revalidated in the background. Revocation, expiry or ownership
loss closes the browser socket without independently hanging up its carrier
call. Grant storage is bounded to 1,024 sessions; capacity is reserved before a
session action can create a call or replace a token. Existing grants can renew
at capacity.

Direct ingress removes supplied `X-Apteva-*` identity headers and reconstructs
only Server-verified project/user/principal metadata. Existing Telephony business
permissions and call admission remain in force. Runtime lifecycle, MCP, internal
peers and application-user login routes are excluded. Call APIs, authorized
operator configuration, recording access and bounded SSE are supported. Calls
are never retried through a second transport after a direct mutation fails.
WebRTC signaling and listen/coach control use the selected signaling base;
WebSocket audio uses the selected media base. WebRTC RTP goes to the execution's
ICE-selected UDP address.

**Current Server limitation:** direct credentials cover platform users with the
required project/global access at the managed Apteva browser origin. Delegated
customer `authProvider` clients explicitly remain on the authorized proxy path.
Supporting additional customer origins/identities requires Server work. Browser
CSP/connect-src must allow the chosen direct HTTPS/WSS origins. Assets retain
content-addressed app installation URLs; no per-frame management HTTP is added.
Reload existing browser clients after changing placement/endpoints; discovery is
retained per client instance. A placement move does not migrate an active call.

## Persistent state and recordings

Managed local originals live beside the SQLite database and use relative
`recordings/call-*.wav` references. After a verified snapshot restore, readiness
checks every retained original is regular and present, with the expected size
when recorded. Activation transactionally rewrites recognized legacy absolute
references; it neither deletes nor re-encodes originals. Missing/unmapped files,
file symlinks and size mismatches block activation. Original playback, variants,
private access and Storage imports continue resolving the same file contents.

The backend/Server must verify the transferred archive digest. File existence
and size checks do not independently prove its content hash. Native Server
snapshots cover the SDK data directory and currently cap transfer at **512 MiB**.
A larger recording/state set needs a backend with an adequate verified transfer
or shared-volume contract before migration. Telephony never silently deletes
recordings to fit this limit.

## Server prerequisites before real migration

1. Install the new managed runtime controller and register a concrete backend
   implementing provisioning, endpoint/network setup, verified transfer and
   confirmed stop. The controller alone does not provision a remote machine.
2. Fix legacy rollback boot authorization: the local Server code accepts a
   legacy process token only for `active`/`retiring` deployments. Rollback starts
   the old binary and calls activate while its status is `starting`/`ready`.
   Its SDK `WhoAmI` callback can therefore fail before it boots. Eligible
   initializing legacy rollback must receive narrowly scoped startup access;
   Telephony deliberately does not bypass that denial.
3. Supply both named endpoints and the execution descriptor browser endpoint,
   raw media configuration, sufficient snapshot capacity and correct origin
   authorization. Start only one SQLite owner.
4. Perform staging-only proxy/direct tests, inbound/outbound SIP and WebRTC
   two-way audio, recordings, call drain, move, failed activation/rollback and
   ownership revocation before enabling a real remote placement.

The local tests validate app behavior using temporary databases, real loopback
sockets and protocol fixtures. They do not establish real backend, DNS/TLS,
carrier or production deployment readiness.
