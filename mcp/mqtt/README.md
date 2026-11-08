# MQTT Broker

Version 0.7.0 adds persistent transport usage and per-scope quotas to durable
acceptance, recoverable sessions and application consumers from 0.6.0. A scope is an opaque identifier with no customer, billing, device, or
application-specific semantics. All changes are contained in this app.

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
not exposed to scoped callers. Diagnostic event/audit counters remain process-local;
transport and acceptance usage persist in SQLite and are enforced per scope.
Retained capacity rejects growth without evicting existing retained state.

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
These protective token buckets remain process-local. The persistent quota
windows below enforce separate budgets that survive restart. Neither mechanism
assigns billing semantics to usage.

A full subscriber outbound queue disconnects the affected subscriber explicitly.
Other broker limits reject connections or packets at their protocol boundaries.
Durable acceptance also reserves every matching QoS 1/2 subscriber obligation
before success. A full offline/in-flight queue rejects the whole publish; it does
not update retained state or partially fan out. Retained cleanup preserves the
stored record when its deletion publish is rejected.

## Durable acceptance and recovery (0.6.0)

A successful MQTT PUBACK/PUBREC, or an HTTP/MCP result with `accepted: true`,
means one SQLite transaction committed the complete message journal record,
retained update/deletion, incoming QoS2 marker, and matching QoS 1/2 delivery
obligations. SQLite uses WAL and `synchronous=FULL`. Keep `/data/mqtt.db` and its
SQLite companion files on persistent storage that honors fsync. This is a
single-broker guarantee, without replication or protection from volume loss.

`mqtt_publish` and `POST /test_publish` return `message_id` and `durable: true`.
The same ID appears in journal records, best-effort bus notifications, and MQTT 5
user property `apteva.message_id`. Publisher-supplied values of that property are
replaced. QoS2 retries within a session reuse the committed record; an ambiguous
QoS1 producer retry may create a second acceptance and ID, as MQTT permits.
Producers still need their own logical identifiers for deduplicating those retries.
QoS0 is journaled if accepted, but has no protocol receipt and no offline delivery.

MQTT 3.1.1 clients use `CleanSession=false`. MQTT 5 clients request a positive
Session Expiry Interval; Clean Start abandons an earlier session. The maximum
expiry is `max_session_expiry_seconds` (default 604800, seven days), negotiated in
MQTT 5 CONNACK. Subscriptions, subscription options, queued QoS 1/2 packets,
incoming QoS2 receipt markers, and outgoing QoS2 PUBREL state recover before
listeners open. Incoming and outgoing packet ID namespaces are independent.
Restored offline expiry remains anchored to the saved disconnect/checkpoint time
across repeated restarts. Explicit MQTT message expiry expires queued PUBLISH
bodies, while QoS2 completion state remains until completion or session expiry.
Credential changes invalidate active and offline recoverable sessions.

Immediate wills use the same transaction boundary. A restart publishes an
interrupted connection's saved, still-authorized will once; clearing its flag is
atomic with acceptance. Delayed wills remain unsupported. A will rejected by a normal quota/capacity limit remains pending in a persisted
session shell and is retried every 30 seconds (or when that client ID reconnects).
It does not disable other scopes. Reconnect with the same ID is rejected until
the pending will can be accepted; explicit credential revocation cancels it.
An actual persistence failure makes storage unhealthy and preserves recovery state.

Persistence failures close connections before success, set `ready: false`, clear
the enrollment endpoint and expose `durability.storage_fault` in `/status` and
`mqtt_status`. Repair storage/capacity and restart the app to resume. Capacity
rejections alone remain retryable and do not latch a storage fault.

Defaults per scope: 100000 journal records, 64 MiB serialized journal data and
64 MiB serialized session/queue data. Configure `max_journal_messages_per_scope`,
`max_journal_bytes_per_scope` and `max_session_bytes_per_scope`. Existing
`max_inflight_per_client` also bounds each subscriber queue. Capacity checks and
persistent journal byte/count accounting share the acceptance transaction.
Durable mutations are serialized; throughput depends on SQLite and the amount
of outstanding session state. Unchanged session rows are not rewritten for every
publish. These journal/session storage capacities remain separate from the transport
quotas below. This app attaches no billing or customer-specific meaning to usage.

## Application consumption

Use MCP `mqtt_consumer` or HTTP `POST /consumers` with the same JSON arguments.
Ownership is derived from the authenticated app installation, delegated subject,
agent, or HTTP operator. An argument cannot select another owner's consumer.
Consumer names are local to that owner and scope. Topic filters use the full
scope topic root. Message bodies are complete `payload_base64`, with properties,
publisher identity, `message_id` and decimal-string `sequence`.

1. Create: `{"action":"create","name":"worker","topic_filter":"scopes/YQ/#","start":"earliest"}`.
   `start` can also be `latest`, or `after` with `after_sequence`.
2. Claim: `{"action":"claim","name":"worker","limit":100,"lease_seconds":60}`.
   Process the returned messages, then persist the application's effects.
3. Acknowledge the whole batch: `{"action":"ack","name":"worker","lease_token":"<returned token>"}`.
   Only the current, unexpired token advances the durable committed cursor.
4. Retry: `{"action":"retry","name":"worker","lease_token":"<token>","retry_seconds":10}`.
   Unacknowledged/expired leases become claimable after restart with the same IDs.
5. Replay: `{"action":"replay","name":"worker","after_sequence":"0"}`.
   This resets the cursor and invalidates existing lease tokens. `status` inspects
   the cursor; `delete` removes a consumer and releases its history pin.

Application delivery is **at least once**. Deduplicate `message_id` in the same
application transaction as side effects where possible; acknowledgment cannot
be atomic with another app's database. Leases default to 60 seconds, allow
1–3600 seconds, and retries allow delays of 0–86400 seconds. Claims return at
most 1000 records and about 4 MiB per page (one complete large record is allowed).
One batch per consumer can be leased at a time; independent consumers each see
their own stream.

`{"action":"history","topic_filter":"scopes/YQ/#","limit":100}` reads paginated
history without a consumer. Omit `after_sequence` to start at the earliest
available record, then pass returned `next_sequence` until `has_more=false`.
Filtering can produce an empty page that still advances the cursor. History is
bounded by `journal_retention_hours` (default 168). Registered consumers pin all
unacknowledged history; the app rejects new publishes when pinned history fills
capacity. Acknowledged/unpinned records are pruned by the retention worker after
the window. Explicit replay into pruned history returns an error (HTTP 410),
with no silent gap. Historical reads may include messages whose MQTT expiry has
passed; MQTT delivery still honors expiry.

The platform event bridge and diagnostic audit feed remain best effort; use the
durable consumer API for reliable application processing. Complete retained
pagination remains separate work.
Existing retained list previews are unchanged; individual retained reads are full.
Before upgrading, back up the app database. Migration 004 is additive and cannot
recover messages or sessions lost before 0.6.0. Reconnect persistent MQTT clients
after upgrade to establish their recoverable sessions.

## Persistent usage and per-scope quotas (0.7.0)

`GET /usage`, MCP `mqtt_usage`, and the `usage` field of `/status` / `mqtt_status`
return only the authenticated ownership scope. They include lifetime counters,
the current quota window, effective limits, connection reservations, current
retained logical storage, and the metering start timestamp. Counter values are
decimal strings to preserve 64-bit precision. Metering starts at migration 005;
traffic from older releases is not reconstructed. Existing retained storage is
backfilled, including legacy data assigned to a scope during migration.

| Quota key | Meaning | Default |
| --- | --- | --- |
| `connections` | Concurrent authenticated MQTT client IDs; same-ID takeover reuses its slot | `max_clients` (1000) |
| `connection_admissions` | Authenticated connection reservations per window, including reconnects/takeovers | 0 |
| `accepted_messages` | Durable acceptances per window, across MQTT, HTTP/MCP, event publishes, and wills | 0 |
| `accepted_payload_bytes` | Payload bytes durably accepted per window, across all publish interfaces | 0 |
| `mqtt_ingress_messages` / `mqtt_ingress_bytes` | Admitted incoming MQTT PUBLISH packets / complete encoded packet bytes per window | 0 |
| `mqtt_egress_messages` / `mqtt_egress_bytes` | Reserved outgoing MQTT PUBLISH packets / complete encoded packet bytes per window | 0 |
| `payload_bytes` | Maximum payload bytes for one ingress publish, including a configured will | `max_payload_bytes` (1048576) |
| `retained_messages` | Current retained messages; replacement of a topic does not add a message | `retain_max_count` (10000) |
| `retained_bytes` | Current logical bytes of retained topic, payload and stored properties | 67108864 |

Zero disables a scope quota; global connection, packet/payload and durable-storage
safety limits continue to apply. Windowed quotas default to unlimited. Set trusted
install configuration `quota_defaults_json` for shared defaults and
`scope_quotas_json` for partial overrides keyed by the exact opaque scope ID:

```json
{
  "quota_defaults_json": "{\"connections\":100,\"accepted_messages\":100000,\"mqtt_egress_bytes\":1073741824}",
  "scope_quotas_json": "{\"project-1\":{\"connections\":20,\"payload_bytes\":65536,\"retained_bytes\":16777216}}",
  "quota_period_seconds": "86400"
}
```

Policies allow only the documented nonnegative integer keys; malformed policies
fail mount. Restart the app after changing quota configuration. Omitted override fields inherit defaults. They are persisted as an
effective policy snapshot on mount, with install configuration authoritative on
restart. MQTT, HTTP and MCP callers cannot raise limits or select another scope
through arguments. `quota_period_seconds` defines UTC-aligned windows from 60 to
2678400 seconds (default one day). An open window's start/end and consumption are
persisted: reconnect, restart, clock rollback and a changed window length do not
refill it. At natural expiry a new window starts; lifetime totals never reset.
Only lifetime and the current window are stored, rather than unbounded history.

Lifetime/current-window metrics include `connection_admissions`,
`accepted_messages`, `accepted_payload_bytes`, `mqtt_ingress_messages`,
`mqtt_ingress_bytes`, `mqtt_egress_messages`, `mqtt_egress_bytes`, and
`quota_rejections`. `peak_connections` is a lifetime high-water mark. Additional
`ingress_packets`, `ingress_packet_bytes`, `egress_packets`, and
`egress_packet_bytes` count all admitted authenticated MQTT packets, including
CONNECT and protocol control. Unauthenticated handshakes, malformed packets, TLS/TCP framing and HTTP/inter-app
transport bytes are not charged to these MQTT packet counters. An admitted
MQTT frame later rejected by ACL or acceptance limits still consumes transport
quota; a frame denied by transport quota does not. Control packets are counted but are exempt from
PUBLISH quotas so ACKs and session completion can make progress.

Usage is **durably reserved before admission/writing**, rather than sampled after
I/O. Retries, retained deliveries, fan-out to each subscriber and replay consume
MQTT transport quota each time; a duplicate incoming QoS2 PUBLISH does not consume
another durable-acceptance quota. HTTP/MCP/event publishes cannot bypass the
shared acceptance/payload quotas. A reservation may survive a crash, partial
write, or failed connection establishment without the peer observing the packet;
this conservative accounting prevents overspending and is not an exact network
interface meter. Counter writes and quota decisions are transactional, including
concurrent callers. Acceptance counters, retained gauges and the journal commit
or roll back together. A metering/storage error prevents success and fails closed.

Quota denial sends MQTT 5 quota-exceeded DISCONNECT/CONNACK (0x97), or a legacy
MQTT 3.1.1 connection refusal (code 3) / close before publish acknowledgment.
Per-scope payload denial uses MQTT 5 packet-too-large (0x95), HTTP 413; other HTTP
publish denials use 429 and retry guidance. Outgoing quota exhaustion closes the
subscriber before writing the denied packet. For a persistent session its QoS
1/2 obligation stays queued for retry after replenishment, until session/message
expiry; clean sessions retain MQTT's normal disconnect semantics. A durable publisher success still means
acceptance/queuing, not delivery to that subscriber. Check `window_ends_at`, then
retry with backoff and jitter. Ordinary quota denials do not latch storage faults.

Retained limits enforce logical stored bytes, excluding SQLite page/index/WAL
and filesystem overhead. They no longer prune old messages. Replacements,
empty retained deletions and MQTT expiry update persistent gauges atomically.
When a policy is lowered below existing storage, accepted data stays intact and
operations that reduce or preserve an over-limit dimension remain possible;
new growth in that dimension is rejected. Journal and session quotas from 0.6.0
continue to apply independently. Read-only usage and durable consumer ACKs remain
available after traffic quota exhaustion.

The broker owns an exclusive local database lock for its lifetime; a second
process cannot clear a running broker's connection reservations. On crash the OS
releases that lock. Startup reconciles stale transport reservations to zero while
leaving counters, current quota windows, offline sessions and retained gauges
intact. Live connections are not restored as active sockets. Keep the database,
its SQLite companion files, and `.mqtt.lock` path on a local persistent volume
with working file locks/fsync; the app is not a multi-broker shared-database cluster.
Do not remove a live lock file. Clean shutdown joins protocol writers/housekeeping
before releasing ownership. In-memory databases are only used by unit tests.


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
