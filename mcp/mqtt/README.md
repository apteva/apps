# MQTT Broker

Version 0.5.0 adds TLS, certificate renewal, public endpoint metadata, and explicit
ingress overload rejection to 0.4.0 ownership isolation and explicit credentials. All
ownership logic lives in this app. A scope is an opaque identifier, without
customer, billing, device, or application-specific semantics.

## Ownership boundary

The SDK integration maps an authenticated project identifier to `scope_id`.
Each scope has a reserved topic root:

```text
scopes/<base64url-without-padding(scope_id)>/
```

For example, scope `project-1` uses `scopes/cHJvamVjdC0x/`. MQTT clients,
management publishes, ACLs, retained reads, and event bridges use **full topic
names**, including that root. A publish to `devices/light/state` must become
`scopes/cHJvamVjdC0x/devices/light/state`.

MCP resolves scope from `sdk.CallerFrom(call).ProjectID`. `_project_id` and
`scope_id` arguments cannot override it. Missing authenticated scope is denied.
A project-scoped installation also rejects a different caller scope.

HTTP routes retain the SDK's install-token authentication. Delegated requests
use the SDK's verified signed principal. Platform operator requests use the
gateway-owned operator/project headers behind that token gate. A fixed project
install can also use its install identity. Global installs require an identified
scope; arbitrary query parameters and body fields cannot select one inside the
sidecar. The platform proxy validates the panel's `project_id` query before
forwarding its trusted identity. Invalid principal signatures never fall back.
Keep the install token at the gateway/service boundary; it is a privileged
service credential, not an MQTT client password.
Both management interfaces fail closed when `APTEVA_APP_TOKEN` is missing,
including standalone development runs.

Users, clients, retained state, message logs, discovery records, subscriptions,
events, and status counters are scope-filtered. Broker-wide usage counters are
not exposed to scoped callers. Diagnostic counters are currently process-local;
durable usage accounting is a later stage. Retained pruning operates separately
within each scope.

## Credentials

There is no seeded default user or logged bootstrap password. Create the first
credential through the authenticated panel, HTTP `POST /users`, or
`mqtt_users_create`. Both ACL arrays must be supplied. An empty array denies
that operation; missing arrays, `null`, malformed arrays, root-wide `#`, and
foreign-scope filters are rejected. This policy applies to all installations.

For example, in authenticated scope `project-1`:

```json
{
  "username": "sensor",
  "allow_publish": ["scopes/cHJvamVjdC0x/sensors/+/state"],
  "allow_subscribe": ["scopes/cHJvamVjdC0x/sensors/+/command"]
}
```

Omitting `password` generates a random password returned in that creation
response only. Alternatively supply a nonempty password of at most 72 bytes
(bcrypt's input limit). Passwords are stored only as bcrypt hashes, excluded
from reads and events, and never logged by the app. HTTP management responses
use `Cache-Control: no-store`.

The returned MQTT username is scope-qualified:

```text
cHJvamVjdC0x:sensor
```

Use that full username when connecting. Local names can be reused independently
in different scopes. Management accepts the local name or the current scope's
qualified name. Supplying another scope's qualified name is rejected. The
legacy `mqtt_users_add` tool is now create-only and cannot replace credentials.

Password rotation preserves the credential ID. Password, ACL, enable/disable,
and delete operations invalidate existing authentication snapshots, disconnect
active clients, and clear their old subscriptions and inflight state. HTTP
`PATCH /users/{username}` accepts one operation per request: ACL update (one or
both arrays), password rotation, or enabled state. No partial mixed update is
performed.

## Sessions and topic authorization

The first authenticated use of a client ID atomically binds it to a scope and
credential ID in SQLite, before the broker can replace an existing client.
Another identity cannot take over that ID, even after disconnect or restart.
Deleting and recreating the same username produces a new credential ID and
does not inherit the old binding. Bindings are deliberately not automatically
released; use a fresh client ID when replacing an identity.

Subscription filters must be contained by an allowed filter. For example,
permission for `.../sensors/+` does not authorize `.../sensors/#`. Shared
subscriptions are checked using their underlying filter. `$SYS` and
cross-scope filters are denied. MQTT 5 response topics must remain in the
authenticated scope. Incoming topic aliases still pass the broker's resolved
topic ACL check; outgoing aliases are disabled so queued and replayed publishes
can be checked against their full topic at encoding time.

Last Will topics require publish permission at authentication and are rechecked
when emitted. Delayed wills are rejected in this stage, since their broker
timer path cannot safely recheck revoked credentials. Discovery topics and
their known state/command/availability/attribute references must stay inside
the same scope.

## Upgrade from 0.3.x

Back up the app database and plan client reconfiguration before upgrading.
This release changes topic names, wire usernames, and anonymous-access policy.

On mount, an idempotent transaction assigns unowned legacy data only when
ownership is known:

- A project installation uses its fixed project scope.
- A global installation can explicitly set `legacy_scope_id` to assign its
  unowned credentials, retained state, and logs. Remove the setting afterwards.
- Without a known assignment, ambiguous rows remain quarantined (`scope_id=''`)
  and cannot authenticate or be read, bridged, or restored into the broker.
- Legacy subscriptions and discovery records with an existing unambiguous
  project owner can be migrated to that scope. The old `default` fallback is
  ambiguous and requires a known assignment.

The migration prefixes legacy topic names, supported ACLs, and wire usernames.
It preserves credential IDs/password hashes, but **disables migrated
credentials**. Review their translated ACLs, rotate any formerly logged default
password, update clients to the full usernames/topics, then explicitly enable
the credentials. Legacy system/shared ACLs are omitted for manual review.
Namespace collisions or invalid legacy ACLs roll back the transaction rather
than overwriting owned data. Legacy usernames in historical audit records remain
historical labels; ownership is stored separately.

Anonymous access is unsupported. An existing `allow_anonymous=true` installation
must change that setting before this version will mount.

## TLS and public enrollment (0.5.0)

TLS is the default. An upgrade from 0.4.0 must configure certificates and an
advertised host, or explicitly choose restricted local plaintext mode. Missing
or invalid TLS configuration fails mount; there is no plaintext fallback.
Only one MQTT listener is started. Runtime port declarations expose 8883 (TLS)
and 1883 (local), but the inactive port has no listener.

For an existing certificate issuer:

```json
{
  "transport_mode": "tls",
  "advertised_host": "mqtt.example.com",
  "advertised_port": "8883",
  "tls_provider": "files",
  "tls_cert_file": "/data/tls/fullchain.pem",
  "tls_key_file": "/data/tls/privkey.pem"
}
```

The certificate must be currently valid, match `advertised_host`, and have a
matching private key. Supply the complete intermediate chain. Clients must
verify both chain trust and the advertised hostname; private issuers require
installing their CA in clients. Never disable client certificate verification.
TLS 1.2 is the minimum; obsolete protocol versions are rejected. MQTT credentials
and scope ACLs remain required inside TLS.

The app reloads renewed files every `tls_reload_seconds` (default 30). Its
certificate issuer renews the source files; replace them atomically. Invalid,
not-yet-valid, expired, or mismatched replacements preserve the last valid pair
and report a reload error. Once the last usable certificate expires, new
handshakes fail and enrollment becomes unavailable. Existing authenticated TLS
sessions are not interrupted merely by loading a replacement. Protect private
key file permissions and back up the source certificate configuration.

For app-owned automatic ACME issuance and renewal:

```json
{
  "transport_mode": "tls",
  "advertised_host": "mqtt.example.com",
  "advertised_port": "443",
  "tls_provider": "acme",
  "acme_accept_terms": "true",
  "acme_email": "operator@example.com",
  "acme_cache_dir": "/data/mqtt-acme"
}
```

ACME uses TLS-ALPN-01. Public TCP **443 must forward unchanged to this app's
internal TLS listener on 8883**, preserving SNI and ALPN `acme-tls/1`. DNS must
resolve to that public endpoint. An HTTP reverse proxy or TLS-terminating proxy
is insufficient for this mode. Routing is operator-managed; the app neither
changes DNS nor takes over another service's port 443. Choose files mode when
an existing ingress/certificate issuer already owns that routing. An alternate
HTTPS ACME v2 directory is configurable; validate with an issuer's staging
directory before production. Automatic provisioning uses ECDSA P-256 certificates.

The default cache is `mqtt-acme` under the SDK per-install persistent data
directory (or an explicit `acme_cache_dir`). It holds ACME account keys and renewed certificates; the
cache directory must be mode 0700 and files are written mode 0600. Back it up
with the app data. Terms acceptance must be explicitly configured. The manager
issues and renews certificates within `tls_renew_before_days` (1–30, default 30)
and retries CA failures. Ordinary client handshakes never perform CA network
requests. The certificate worker proactively loads/provisions the certificate;
enrollment stays pending until it is valid. Public CA failures and certificate
expiry never cause plaintext fallback.

`mqtt_status` and HTTP `/status` return the external `advertised_host`, external
`advertised_port`, `tls`, and `mqtts://...` endpoint. `port`/`bind_address` describe
the actual listener independently, including NAT/forwarding cases. An unavailable
certificate produces `ready: false` and an empty `endpoint`, preventing enrollment
against a falsely ready endpoint. `transport.certificate` reports provider,
expiry, renewal due state, and load errors. Hostnames cannot contain schemes,
ports, paths, wildcards, or wildcard bind addresses. IPv6 hosts are correctly
bracketed in endpoint URLs. There is no container-hostname inference.

For a trusted local network only, explicitly set `transport_mode: local`.
It defaults to loopback port 1883. An explicit private bind IP and private
advertised IP can expose a LAN endpoint. Public/wildcard bind addresses and
public advertised hosts are rejected. Local mode's default advertised port is
1883; do not set the TLS port unless a private TCP forward actually provides it.
No TLS is claimed in local enrollment metadata. Ports other than the declared
1883/8883 are rejected for managed listeners; port 0 is only for local tests.

Pending TLS handshakes and connections awaiting MQTT authentication are bounded
by `tls_max_pending_handshakes` (default 128). Handshake/CONNECT waits are bounded
by `tls_handshake_timeout_seconds` (default 15). Completing TLS without MQTT
authentication does not release this capacity. Service cancellation closes the
listener, active connections, and certificate worker.

## Overload and retry contract (0.5.0)

Ingress rate admission runs **before routing, retained mutation, PUBACK, or
PUBREC**, for MQTT 3.1.1/5 and QoS 0/1/2. A rejected publish has no successful
acceptance acknowledgment and produces no retained value or application event.

| Surface | Capacity exhaustion |
| --- | --- |
| MQTT 5 | DISCONNECT reason `0x97` (quota exceeded), then close; no successful publish acknowledgment |
| MQTT 3.1.1 | Close TCP/TLS before a publish acknowledgment; the protocol has no negative PUBACK |
| HTTP publish / retained deletion | HTTP 429 with `Retry-After: 1` |
| MCP / inbound app publish request | An explicit capacity error, without a success result |
| Oversized HTTP payload | HTTP 413; MCP returns a size error; oversized network packets close before acceptance |

Wait at least the HTTP retry delay and use exponential backoff with jitter for
reconnect/retry. A retry delay does not reserve capacity. QoS 0 has no receipt:
applications requiring confirmation must use an application acknowledgment and
stable identifiers. Do not equate reconnection with successful prior delivery.

`max_publish_per_second`/`max_publish_burst` limit individual client connections.
`max_publish_per_scope_per_second`/`max_publish_per_scope_burst` (defaults
1000/2000) share admission across all credentials, connections, and HTTP/MCP
publishes in an ownership scope. Reconnecting does not reset that scope's bucket.
A rate of zero explicitly disables its limiter. Invalid policy values fail mount.
Budgets are process-local and are not durable usage or billing counters.

A full subscriber outbound queue disconnects the affected subscriber explicitly.
Other broker limits reject connections or packets at their protocol boundaries.
Ingress admission does **not** reserve every subscriber's inflight storage or
establish durable delivery. Retained cleanup preserves the stored record when
its deletion publish is rejected, so a failed cleanup cannot partially delete it.

## Remaining delivery guarantees

Event and audit queues remain best effort. This release does not add durable
message acceptance, MQTT session/offline/inflight recovery, acknowledged app
consumption/replay, persistent usage accounting/quotas, or complete retained
pagination. Retained writes still need stronger storage-error propagation.
A successful MQTT acknowledgment or HTTP/MCP publish result proves ingress was
admitted; it does not prove disk durability, delivery to every subscriber, or
processing by a consuming application. Application receipts and retries remain
necessary for stronger end-to-end guarantees until that later work is complete.

## Validation

Run independently of the workspace overlay so the published SDK pin is tested:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
```

From the apps repository, rebuild this panel with:

```sh
bun run scripts/build-panels.ts --app mqtt
```
