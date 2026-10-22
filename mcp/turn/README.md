# TURN

Standalone STUN/TURN infrastructure app for Apteva. It runs its own Pion TURN
server and issues temporary WebRTC ICE credentials. It has no dependency on
Telephony or any other app, and imports no Server internals.

Version 0.1 provides IPv4 UDP relay allocations through UDP/TCP client
connections on port 3478 and TURN over TLS on TCP 5349. IPv6 and RFC 6062 TCP
relay allocations are not implemented. TCP/TLS are client-to-relay transports;
media between TURN and its peer remains UDP. WebRTC media retains its own
DTLS/SRTP encryption.

## Native management panel

The manifest registers `ui/TurnPanel.mjs` in `project.page`, like CRM. The
default-exported React component receives Apteva installation/project props,
uses the host React and production JSX modules, and follows host palette,
font, monospace, radius and semantic status tokens. It does not create a second
React root, iframe, theme selector or standalone site. Its compact tab navigation
and scrolling body remain inside the existing dashboard panel.

Overview shows relay state, traffic, allocations, endpoint URLs, TLS certificate
expiry/fingerprint and required ports. Settings provides endpoint/capacity
controls, certificate/key upload or multiline PEM entry, and optional signing
secret replacement. Credentials creates, masks, copies and clears temporary ICE
credentials for a project/session. Requests select the exact installation and
project, and settings validate before the platform config API persists them.
Stored secrets are never loaded into the form state. Theme changes apply through
the host CSS cascade, including clean/terminal and light/dark modes.

## Running on a separate instance

Use Apteva's managed app deployment controller with a registered execution
backend supporting source/binary packages and raw TCP/UDP listeners. The
Instances native backend supports this shape: a Go binary on a registered Linux
amd64 or arm64 host, with the HTTP control plane tunneled back to the management
server. TURN traffic goes directly to the relay host, independently of that
tunnel. An HTTP ingress proxy alone cannot transport the TURN data plane.

The manifest declares a CGO-disabled portable package, protected runtime
lifecycle endpoints, exclusive activation, and every additional network port.
No coturn installation, system libraries, external database, Docker socket, host-specific
path, or shared Telephony filesystem is required. Signing configuration remains
in Apteva's encrypted installation settings and is injected at runtime.

Configure the installation before deploying:

| Setting | Value |
| --- | --- |
| `public_ip` | Public IPv4 of the instance **running TURN**, rather than the management server |
| `public_host` | Optional DNS hostname resolving directly to that instance; defaults to `public_ip` |
| `shared_secret` | At least 32 randomly generated characters, entered as a password setting |
| `tls_certificate_pem` | Optional PEM certificate chain or base64-encoded PEM covering `public_host` |
| `tls_private_key_pem` | Matching PEM private key or base64-encoded PEM; encrypted password setting |
| `bind_ip` | `0.0.0.0` by default; optionally a local IPv4 on the target host |
| `allowed_peer_cidrs` | Optional narrower list of public peer networks, such as the Telephony hosts |

Keep the HTTP control port private; let Apteva and the selected backend manage
its authentication and reachability. The app honors `APTEVA_APP_PORT` and
`APTEVA_BIND_HOST` through the SDK. Those HTTP settings do not change the raw
TURN sockets.

The relay host's firewall and provider security group must permit:

| Ports | Purpose |
| --- | --- |
| UDP 3478 | STUN and TURN client traffic |
| TCP 3478 | TURN client traffic over TCP |
| TCP 5349 | TURN over TLS |
| UDP 49160–49223 | Public relay endpoints and returning peer traffic |

Allow outbound UDP to the intended public media peers. If the instance is behind
NAT, use a static public mapping preserving these same ports. Raw TURN listeners
must bypass HTTP/CDN proxies. `public_ip` and DNS must reflect the target's actual
public address; the app does not infer it from the management gateway.

The pool is deliberately fixed for this release. Every UDP pool socket is bound
on startup and remains bound between allocations and in standby/drain. This
satisfies the native backend's per-port/PID readiness and confirmed-stop checks.
Any occupied pool port aborts startup and releases the partially acquired pool.
Changing an install setting cannot bind ports omitted from the manifest.

Only one installation can own this port set per host. Use another host/public IP
for another independent relay. This release is not replica-safe and does not
opt into autoscaling or active/active placement.

For an existing TURN installation, a typical prepare request is:

```http
POST /api/apps/installs/<turn-install-id>/deployment/prepare
Authorization: Bearer <administrator-api-key>
Idempotency-Key: <unique-operation-key>
Content-Type: application/json

{
  "placement": {
    "type": "backend",
    "backend": {
      "app_install_id": 52,
      "backend_id": "native"
    },
    "target": {
      "app_install_id": 52,
      "resource_type": "instances.instance",
      "resource_id": "6"
    },
    "gateway_url": "https://apteva.example.com",
    "options": {}
  }
}
```

The IDs are illustrative: select the actual registered backend and an accessible
instance resource from its target catalog. Run deployment preflight against that
target, prepare the installation, inspect its operation, then activate through
`POST /api/apps/installs/<turn-install-id>/deployment/activate` with a fresh
idempotency key. Server owns the build, package delivery, execution credentials,
port reservations, HTTP routing, rollback, and lifecycle ordering. This app does
not create infrastructure or change firewall rules itself.

The release manifest pins its source to the immutable `turn/v0.2.1` tag. The
registry entry, manifest and icon resolve to that same tag. Installing the app
does not deploy it into an existing Telephony instance or change another app.

## TURN over TLS

Choose **Automatic through Domains and Let's Encrypt** in the native Settings panel.
Bind Domains 1.2.0 or newer in the TURN installation's dependencies, set
`public_host` to a fresh subdomain and select its `dns_zone`. For a global
installation, set `dns_project_id` to the project owning that zone. Accept the
Let's Encrypt subscriber agreement (`acme_accept_terms=true`). Contact email
(`acme_email`) is optional.

TURN calls Domains through the authenticated Apteva SDK; provider credentials
stay with the management platform. It creates the subdomain's A record pointing
to `public_ip`, creates a temporary DNS-01 TXT record, waits for both public
resolvers to see the correct values, and obtains a public certificate. It
rejects conflicting A, AAAA and CNAME records instead of replacing them. When
moving to a new public IPv4, it updates only its own journaled A record using
the exact provider ID and previous-value snapshot. DNS
challenge records use append-only creation; cleanup targets only the exact
record ID and value this installation created. Failed cleanup is journaled and
retried. Existing challenge delegations are preserved and reported as a conflict.
The A record stays in DNS when TURN is stopped or removed; inspect it before
manually deleting a retired relay hostname.

The panel generates `tls_storage_key` when managed TLS is first enabled. Keep
this stable secret in encrypted installation settings. ACME account and private
certificate keys are AES-GCM encrypted with it in the app's private SQLite DB.
Keep both the storage key and app data when moving or restoring the installation.
Changing the credential signing secret does not affect TLS encryption. There is
no shared filesystem or private-key export from another app.

Only the active execution performs issuance and renewal. Certificates are checked
hourly and renewed with less than 30 days remaining. Failures retain the previous
certificate and use a one-hour retry backoff; the UI shows the current phase.
Renewal atomically replaces the leaf for new TLS handshakes without interrupting
existing connections or allocations. TCP/UDP continue to work during initial
issuance and renewal failures. No public HTTP port or ingress proxy is required
for DNS-01 validation. The management gateway and Domains app must remain reachable.

**Import a certificate** remains available for operators supplying their own
certificate chain and matching private key. The server enforces TLS 1.2 or newer
and validates the key pair, hostname, current validity and server-auth usage.
Combined certificate/key input is limited to 64 KiB. Manual certificates must
be renewed by the operator.

TCP 5349 remains bound even before certificates are configured, preserving
managed port ownership. Without valid material, TLS handshakes fail and no
`turns:` URL is advertised. The panel clearly reports setup required; UDP/TCP
remain usable. Once configured, credential responses include
`turns:<public_host>:5349?transport=tcp`. Expired material is refused on new
handshakes. Expired imported material also fails readiness; managed mode keeps
UDP/TCP ready so automatic renewal can recover. An invalid replacement is
rejected by settings validation. Managed mode renews automatically; imported certificates require manual replacement.

The protocol tests establish a trusted test CA, verify hostname and chain with
the client, allocate through TLS, relay audio both ways, and confirm fencing
closes encrypted sessions and releases the TLS listener.

## API and future consumers

Protected routes, normally accessed through the authenticated Apteva app proxy:

| Route | Purpose |
| --- | --- |
| `GET /status` | State, advertised endpoint, allocation capacity, aggregate byte counters |
| `POST /credentials` | Issue credentials for an authorized project and session |
| `POST /settings/validate` | Validate a settings patch and certificate without persisting or returning secrets |
| `GET /ready` | Minimal unauthenticated deployment readiness; no configuration or credentials |

For a project installation, `project_id` defaults to its fixed project and a
different project is rejected. A global installation requires an explicit project
on HTTP requests. The HTTP route is an administrative interface protected by the
installation token; callers holding it must enforce project/end-user access.
It must never be exposed as an anonymous browser credential service.

```json
{
  "project_id": "project-a",
  "session_id": "call-or-peer-session-id",
  "ttl_seconds": 3600
}
```

The response includes `expires_at` (Unix seconds), `ttl_seconds`, and
`ice_servers`, an array suitable for `RTCPeerConnection({ iceServers: ... })`.
Each entry has `urls`, `username`, and `credential`. The secret is never returned.
Usernames contain an expiry plus hashes of project/session identity and a random
nonce; plaintext customer or call identifiers are not exposed on the wire.
Passwords follow the standard TURN REST HMAC-SHA1 convention.

The same credential service is available as the **app-only** MCP tool
`turn_credentials`, with `session_id` and optional `ttl_seconds`. Project context
comes from the SDK's authenticated app-to-app dispatch, not a tool argument.
`turn_status` is a normal read-only MCP tool.

A future consumer should declare an optional app integration for `turn`, authorize
its own user/session, then call `turn_credentials` with `CallAppResult`. It should
cache credentials only until expiry and supply them during ICE negotiation.
This change does not modify or bind Telephony, or any other app.

## Authentication and capacity

The maximum credential lifetime defaults to one hour and is configurable from
60 seconds to 24 hours. Requests may ask for a shorter lifetime. New credentials
must be obtained before expiry; this is an authentication expiry, not an instant
termination of an existing TURN allocation. Allocation refreshes authenticate
again and each allocation has a maximum ten-minute refresh lifetime.

Defaults are 64 concurrent allocations across all transports, four allocations
per issued credential, 1 MiB/s of combined upload/download per allocation, and
credential issuance limited to ten requests/second with a burst of twenty.
Allocations are not the same as calls: a client may consume several allocations
while negotiating ICE. Exceeding the bandwidth budget drops datagrams rather
than building an audio backlog. The UDP receive queues and TCP connection count
are bounded. Byte counters cover application UDP payloads in both directions,
not billable network traffic, packet headers, or the client control connection.

Peers must be public IPv4 addresses. Loopback, private networks, link-local and
cloud metadata addresses, CGNAT, multicast, reserved networks, and the relay's
own public IP are denied. An allowlist can narrow this policy but cannot grant
access to private/internal peers. Put TURN on a different public IP from its
media peers. Browser credentials cannot be used to reach private Telephony
addresses or the relay host's local services.

## Deployment lifecycle

`/_apteva/runtime/app/describe`, `/status`, `/activate`, and `/drain` implement the
generic managed-app contract and require the installation's app token. Managed
startup validates deployment ID/generation and begins in standby. Every listener
is bound for readiness, but standby answers no TURN/STUN traffic and issues no
credentials. Activation arms the execution; admission begins only after the
gateway confirms matching active ownership.

The ownership lease is renewed every second using `APTEVA_OUTBOUND_TOKEN` (or
the app token on older runtimes). Gateway callbacks require HTTPS, except for
loopback tunnels. A transient outage retains the current lease until expiry.
Revocation, an ownership mismatch, or expiry fences the execution and closes the
listeners, accepted TCP connections, and allocations. A fenced process requires
a restart; it cannot reactivate itself later.

Drain stops credential issuance and new allocations while existing media and
authenticated refreshes continue. It reports `drained: true` only after all
allocation leases close. Retry the drain after clients finish, or cancel/resume
through the deployment controller. An adopted legacy process validates its
deployment identity with the gateway before accepting lifecycle mutations.

TURN allocations and network sockets cannot move between hosts. Schedule moves
or upgrades after draining, give the destination its correct public IP/DNS
settings, and have consumers obtain credentials for new sessions. This app has
no persistent media session state to transfer. Managed TLS account/certificate
state is stored in the app database and must move with the installation and its
unchanged `tls_storage_key`. No zero-interruption call
migration is promised.

## Development and verification

The workspace's `go.work` does not yet enumerate this new app. Use its published
SDK pin without changing other repositories or the workspace overlay:

```sh
cd /Users/marcoschwartz/Documents/code/apps/mcp/turn
GOWORK=off go test -race ./...
bun test ui/*.test.ts
GOWORK=off CGO_ENABLED=0 go build -o /tmp/apteva-turn .
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/apteva-turn-linux-amd64 .
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/apteva-turn-linux-arm64 .
```

For a manual local process, set `APTEVA_APP_TOKEN`, `APTEVA_PROJECT_ID`, and
`APTEVA_APP_CONFIG` (JSON string map containing the required settings). Supply the
token in `Authorization: Bearer ...` to the protected HTTP endpoints. The app
refuses to start without an app token, including in local development.

Tests exercise real authenticated UDP, TCP and TLS relay traffic, bad credentials,
expiry validation, project separation, atomic quotas, bandwidth drops, peer
restrictions, standby behavior, drain continuity, ownership fencing, complete
port ownership, and release of sockets on shutdown. Local loopback relay access
exists only in the in-process test harness, never as an installation setting.
Target firewall/NAT reachability still needs an external ICE allocation/media
check after deployment; local readiness does not prove public connectivity.
