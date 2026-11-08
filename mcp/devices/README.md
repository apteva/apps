# Devices

Devices 0.3.0 is Apteva's project-scoped device control plane. It owns device
resources, structured data, operation capabilities, recoverable enrollment,
command execution tracking, durable events, and usage measurements. MQTT
**0.6.0 or newer** owns the broker, topic authorization, credentials, and sessions.
SaaS maps customers to authorized projects and supplies operational limits; no
customer, product, billing, or Thinglane concepts are built into Devices.

## Ownership and MQTT integration

The ownership scope is the gateway-authenticated project ID. Devices remains a
project installation with its own database. MCP requires `sdk.CallerFrom(call)`
and a matching `ProjectID`; model arguments cannot choose another scope. HTTP
uses the SDK install-token gate, verifies delegated signed principals, and
rejects foreign project headers. Missing install tokens fail closed. Incoming MQTT data is consumed from the authenticated app-owned durable MQTT consumer. Scoped topics and the enrolled device's exact publisher username are checked before processing. Connection events still require source app `mqtt` and matching ownership metadata.

Arbitrary customer subscopes within one project are not a delegation mechanism.
Use separate authorized projects. Any future independent scope delegation must
be authenticated consistently by the gateway, SDK, Devices, and MQTT.

Every topic has MQTT's `scopes/<base64url(scope_id)>/` root. Enrollment returns the
exact qualified MQTT username and these complete topic names:

| Suffix | Device permission | Purpose |
| --- | --- | --- |
| `commands` | subscribe | Command requests |
| `receipts` | subscribe | Application acceptance of data messages |
| `response` | publish | Command results |
| `state` | publish | Current-state patches or snapshots |
| `telemetry` | publish | Historical samples |
| `manifest` | publish | Capability descriptors; retain recommended |
| `availability` | publish | Retained availability and Last Will |

Use returned credentials and topics verbatim; the username is independent of
the device ID. Credential names are private to an enrollment operation, avoiding
collisions with manually managed MQTT users. MQTT client IDs are durably bound
to credential identities: use a new client ID when replacing a credential.

## Recoverable lifecycle

`devices_provision`, `devices_rotate_secret`, `devices_enable`,
`devices_disable`, and `devices_delete` accept `idempotency_key`. HTTP accepts
`Idempotency-Key` as well; conflicting body/header keys are rejected.

Intent, arguments, request fingerprint, and a chosen encrypted secret are
committed before MQTT calls. Pending operations are retried by the reconciliation
worker after errors or restart. A completed local transition, its event, and its
usage measurement commit together. Repeating a key with different arguments
returns an idempotency conflict. MQTT calls are convergent retries, not a
cross-database transaction; a repeated rotation can disconnect sessions again
while applying the same chosen password.

Results include `operation_id`, `status` (`pending` or `completed`), and operation
error details. Pending is accepted work, not successful enrollment. Poll
`devices_operation_get` or `GET /operations/<id>`. Never provision a second
identity to recover an ambiguous first request.

Provisioning uses the supplied device ID as a natural retry key when no key is
provided; an omitted ID is derived deterministically from the name. Rotation
without a key is an intentional new rotation. Always provide a key when rotation
may be retried. Enable/disable retries preserve desired state while allowing
later alternating operations. Conflicting pending operations on a device fail.

Passwords are encrypted with AES-GCM and recoverable by the same authorized
management surface for `secret_recovery_hours` (default 24, maximum 168) after
completion. They are excluded from events, usage, and logs. Expired results give
an explicit rotation recovery action; superseded passwords are not disclosed.
HTTP responses use `Cache-Control: no-store`.

**Back up `devices-recovery.key` with the database.** It is generated mode 0600
next to the database and is independent of the install token. Restoring encrypted
operations without this key fails closed. Pending operations retain their
ciphertext until they complete; completed ciphertext is removed after expiry.

Disabling/deleting blocks new local commands and ingestion as soon as intent is
recorded. Remote revocation is complete only when the operation completes.
Active commands become cancelled with execution unconfirmed. Deletion removes
credentials, known retained device topics, device state/data, and command rows; command response diagnostics,
usage and durable events survive for their own retention windows. A tombstone
prevents reuse of the retired device ID.

## Generic data contract

Enroll `protocol: apteva.devices/v2` for arbitrary JSON data. Publish this
envelope to `state` or `telemetry`:

```json
{
  "message_id": "boot-42-sample-123",
  "observed_at": "2026-11-08T09:00:00Z",
  "session_id": "boot-42",
  "sequence": 123,
  "mode": "patch",
  "data": {"id": "a-value", "name": "also-a-value", "nested": [true, null, 23.5]},
  "metadata": {"sensor": "room-a"}
}
```

`data` may be an object, array, scalar, or null. Application fields including
`id`, `name`, `timestamp`, and `uptime` are preserved. Metadata never becomes a
state value. State object patches replace supplied top-level values, preserve
omitted keys, and store null as a value. `mode: snapshot` replaces all state,
including an empty object clearing it. Top-level state keys must contain 1–128 characters. Non-object data is a snapshot under the
current-state key `$`. Telemetry is append-only by default; set `update_state:
true` to apply it to current state too.

Message IDs are unique per device across state/telemetry. Identical retries
produce one history sample, usage record, and logical event; changed content
using the same ID conflicts. History, deduplication, state, usage, and outgoing
event records commit in one SQLite transaction. Invalid or over-limit messages
have no partial state effects. The server also records `received_at` (MQTT broker acceptance time for durable MQTT records; HTTP arrival/ingestion time for HTTP submissions).

Within one session, increasing sequence is authoritative. Across sessions or
without sequence, normalized `observed_at` and message ID order determine the
latest state update for the device. Older messages remain in history but do not
overwrite current state. New session IDs are required after sequence reset.
`observed_at` must be RFC3339; future clock skew defaults to 300 seconds. Keep a
synchronized clock for both data and command expiry.

After a successful commit (including an identical logical duplicate), MQTT
clients receive `{"message_id":"...","status":"accepted"}` on their receipt
topic. HTTP clients receive the equivalent acceptance in the response body.
Receipts never precede the Devices database commit.

Devices uses MQTT 0.6's app-owned `mqtt_consumer` named
`devices-ingestion-v1`. It claims leased batches, commits each message's effects
and transport receipt together, and acknowledges the whole batch only after all
records are processed. A lost broker acknowledgment or process restart can
redeliver messages; the stable broker ID suppresses repeated effects, including
native v1 telemetry and manifest events. Logical v2 IDs additionally suppress
producer retries and duplicate submissions across MQTT and HTTP.

Permanent invalid messages are durably quarantined with a sanitized reason and
a `devices.data.rejected` event, then acknowledged so they cannot block valid
traffic forever. Capacity/storage failures leave the batch unacknowledged and
request retry. Unacknowledged broker history stays pinned, so persistent failure
can explicitly backpressure MQTT capacity; inspect broker/Devices diagnostics
and raise limits or repair storage rather than deleting a consumer cursor.
Acknowledged transport receipts default to seven days of retention, with a
one-million-row safety ceiling. Rejection diagnostics default to the newest
10,000 acknowledged records; pending diagnostics are retained. Keep the receipt
retention at least as long as any intended broker replay window.

Reliable clients still persist logical samples until application receipts:
MQTT PUBACK confirms broker persistence, not Devices ingestion. V1 clients gain
durable broker-to-Devices delivery but do not gain producer-side logical
identifiers or exactly-once hardware execution. Logical deduplication lasts
while device message history is retained; keep producer retries within that
window. Platform data notifications are no longer the ingestion source.

## Device-authenticated HTTP/HTTPS ingestion

The write endpoints are `POST /ingest/<device_id>/state` and
`POST /ingest/<device_id>/telemetry`, under the installation's normal proxy:

```text
https://your-server/api/apps/devices/_install/<install_id>/ingest/<device_id>/state
```

Use HTTPS at the platform ingress; local loopback HTTP is suitable for local
tests. These two routes deliberately bypass the platform/SDK management-token
gate and authenticate a separate device-only write credential themselves. All
other routes retain management authentication. Prefer `X-Device-Token`; bearer
credentials are also understood when the proxy preserves them. Never give a
device `APTEVA_APP_TOKEN` or a platform operator API key.

Issue a credential with `devices_ingest_credential_create` or
`POST /devices/<device_id>/ingest-credentials`:

```json
{"label":"Sensor HTTPS uploader","idempotency_key":"enroll-http-1","expires_in_seconds":2592000}
```

The response contains a `token`, credential metadata, recovery deadline, and
relative ingestion path. Credentials are 256-bit random secrets verified by
hash and constant-time comparison, with encrypted recovery during the same
bounded secret-recovery window used for enrollment. A repeated issuance key
returns the same result; changed reuse conflicts. The token can write only the
named device's state/telemetry, never read history, invoke commands, change
capabilities, issue credentials, or modify policy. Scope comes from the fixed
installation and credential binding, not JSON arguments.

Submit the same v2 envelope described above. The path/body device and scope
must agree with the credential; a foreign device is denied. The generic data
contract is available for HTTP submissions to native v1 resources as well.

- New logical message: HTTP 201 with `accepted`, `message_id`, `duplicate:false`,
  device/scope, and original `received_at`.
- Identical retry, including a message first ingested over MQTT: HTTP 200 with
  `duplicate:true`; no repeated usage or state effect.
- Changed logical ID reuse: HTTP 409.
- Capacity exceeded: HTTP 429 and `Retry-After: 1`; no acceptance receipt.
- Invalid/revoked/expired/disabled credentials: HTTP 401. Invalid envelopes:
  HTTP 400. Bodies above the hard 128 KiB protocol maximum: HTTP 413.
- Storage failure: an error response, with no partial data or acceptance.

An accepted identical retry remains recoverable after limits are lowered.
Credential revocation takes precedence over receipt replay. Disable/rotation
intent immediately blocks HTTP submissions; completed disable or rotation
revokes all existing HTTP credentials. Enabling a device does not resurrect
revoked credentials. Deletion removes credentials. List metadata with
`devices_ingest_credentials_list` or GET on the credential collection; revoke
with `devices_ingest_credential_revoke` or
`DELETE /devices/<device_id>/ingest-credentials/<credential_id>`.


## Capabilities and adapters

A generic capability manifest describes available operations independently of
any hardware vocabulary:

```json
{
  "protocol": "apteva.devices/v2",
  "operations": [{
    "name": "lamp.set",
    "input_schema": {
      "type": "object", "required": ["on"],
      "properties": {"on": {"type": "boolean"}},
      "additionalProperties": false
    },
    "output_schema": {"type": "object"}
  }]
}
```

Generic commands must match an advertised operation and its input schema.
Successful responses are checked against the output schema captured at command submission when supplied.
Schemas are compiled by jsonschema/v6; external references cannot load network
or filesystem resources. A manifest cannot change an enrolled protocol.

The `apteva.devices/v1` adapter preserves native variables/functions and GPIO
helpers, their wire aliases, field conventions, and deny-by-default pin policy.
The generic resource/command/ingestion layers do not require those concepts.

## Commands and client obligations

`devices_command_send` durably submits a command and defaults to `wait: false`.
HTTP submission returns 202 while active. Inspect `devices_command_get` or
`GET /commands/<id>`; optional `wait: true` waits until the same durable deadline.
`queued` means accepted; `sent` means a dispatch attempt was started, including
an ambiguous transport result. Final statuses are `succeeded`, `failed`,
`timed_out`, and `cancelled`.

The dispatch worker resumes after restart, reserves configurable concurrency
slots, and retries the same command ID until its absolute deadline. A key is
scoped to the device and fingerprints operation, target, arguments, and timeout;
changed reuse conflicts, including concurrent submissions.

```json
{
  "protocol": "apteva.devices/v2", "command_id": "cmd_...",
  "operation": "lamp.set", "target": "", "arguments": {"on": true},
  "timeout_ms": 10000, "expires_at": "2026-11-08T09:00:10Z",
  "expires_unix_ms": 1794128410000
}
```

Clients must:

1. Reject expired commands before acting, using their synchronized clock.
2. Durably claim a command ID before physical execution and reject conflicting
   requests that reuse it.
3. Cache and replay completed responses on duplicate delivery.
4. Report an interrupted claim as execution unconfirmed; never automatically
   repeat an action whose outcome is unknown.
5. Retain claims/results through expiry plus their delivery/replay margin.

A response echoes `command_id` and contains `success` or a terminal `status`,
with `result` and/or `error`. Terminal outcomes cannot be reopened. Responses
arriving after deadlines cannot succeed even before the timeout worker runs.
Distinct late responses are retained for diagnostics; exact duplicates are
suppressed. Timeout/cancellation does not prove a physical action never ran.
There is no exactly-once hardware guarantee without device-side cooperation.

Native v1 clients only receive automatic redispatch if their manifest advertises
`command_contract: apteva.commands/v1`. Older native clients get one attempt;
an ambiguous outcome times out instead of risking automatic reexecution.

## Query and event APIs

| MCP tool | HTTP route | Result |
| --- | --- | --- |
| `devices_state_query` | `GET /state` | Current values, observed/receipt times, key cursor |
| `devices_data_list` | `GET /data` | Structured history and metadata |
| `devices_events_list` | `GET /history/events` | Durable event replay (`/events` is SDK delivery) |
| `devices_usage_list` | `GET /usage` | Durable usage measurements |
| `devices_responses_list` | `GET /responses` | Accepted and late command response history |
| `devices_limits_get` | `GET /limits` | Active operational limits |
| `devices_usage_summary` | `GET /usage/summary` | Exact window totals and current resource counts |
| `devices_policy_get` | `GET /policy` | Effective live policy, revision, and over-limit status |
| `devices_policy_set` | `PUT /policy` | Authorized revision-checked live policy update |
| `devices_operation_get` | `GET /operations/<id>` | Lifecycle status and bounded secret recovery |

History queries accept `device_id`, `kind`, `from`, `to`, `limit` (1–500), and
`cursor`, returning `{items,next_cursor?}`. `kind` filters data kind, event topic,
usage measurement, or response disposition as appropriate. Times are RFC3339;
response/event/usage times are receipt/creation times. History cursors capture a
high-water mark and are bound to scope and filters. Expired retained history
returns HTTP 410; restart from the available history. Current-state pagination
is a live view. Existing device/state/command list and lookup APIs remain.

Events carry stable `event_id`, `scope_id`, `device_id`, `occurred_at`, and
`schema_version`. Mutation events use a transactional outbox, retrying until the
platform acknowledges acceptance. Consumers deduplicate by event ID and poll
history to recover missed bus notifications within the replay window. Platform
acceptance is not proof that every downstream subscriber processed an event.

Usage records measure `data.messages`, `data.bytes` (accepted wire payload bytes),
`commands.submitted`, command terminal outcomes, and lifecycle operations. They
are scoped and deduplicated independently per device, measurement, and source
ID. Transport retries and rejected data do not increase accepted usage. Pricing,
entitlements, invoices, customer attribution, and commercial policy stay in SaaS.

## Aggregated usage and resource counts

`devices_usage_summary` / `GET /usage/summary` accepts RFC3339 `from`, `to`,
and optional `measurement`; the default is the preceding 24 hours. Windows are
`[from,to)`, using persisted usage-record creation time. Totals group by
measurement in the authenticated ownership scope and are exact decimal strings,
so JSON/JavaScript clients cannot silently round large counts. Independent
queries never expose another scope; an authorized external adapter can combine
summaries across scopes if needed.

The same database snapshot reports devices (enabled/online), pending provisions,
queued/in-flight commands, state keys/logical bytes, history records/logical
bytes, pending events, active HTTP credentials, durable transport receipts, and
quarantined messages, plus per-device maxima used by policy checks. Logical
payload bytes are not filesystem allocation or broker transport bytes. Current
resource counts and historical usage have different semantics; deletion lowers
resource counts without erasing retained usage.

`coverage` reports v0.3 tracking start, earliest retained record, the monotonic
pruning watermark, and whether the requested window is fully covered. Existing
v0.2 usage is included when retained but is conservatively marked incomplete
before v0.3 tracking began. Extending retention cannot recreate previously
pruned history. SaaS must distinguish incomplete coverage from zero activity.

## Limits and retention

Installation configuration supplies the trusted base policy. Durable live
overrides take precedence and survive restart. Clients cannot override limits
per submission. `GET /limits` returns effective settings. Invalid base policy
fails mount; invalid live updates fail before persistence or activation.

`devices_policy_set` is restricted to a bound application whose authenticated
SDK caller name matches `policy_controller_app` (default `saas`) and whose
project matches the installation. Supplying an app name or scope in arguments
does not authorize a caller. Ordinary agent and device credentials are denied.
`PUT /policy` additionally supports explicitly configured platform operator IDs
from `policy_admin_user_ids`; empty (the default) disables HTTP policy writes.
These are installer-controlled settings, never client-provided identity headers.

```json
{
  "expected_revision": 0,
  "request_id": "plan-change-123",
  "limits": {"max_devices": 25, "max_concurrent_commands": 5}
}
```

Updates are positive-integer patches to known operational keys, validated
against hard safety bounds. JSON fractions, unknown/commercial fields, and
out-of-range values fail. An atomic commit records the new revision, overrides,
request fingerprint, and outbox event before the running process swaps its
policy snapshot. Admissions serialize with updates; a successful update is
immediately effective without a restart. Stale revisions or changed request ID
reuse return 409. An identical request replay returns its applied snapshot and
the current revision. Completed request replay records use the operation
retention window; stale revisions still prevent old requests from reapplying
after their replay record expires.

Lower capacity limits preserve devices, stored state, queued commands, and
active commands. Responses report `over_limit`; additional consumption is
rejected until usage drops or policy increases. Already active commands finish,
and new dispatch waits for concurrency to fall below the new cap. Accepted
identical data retries still get their original receipt. Retention changes are
separate explicit cleanup policy: they apply at the hourly cleanup, preserving
current state, pending work, and unacknowledged transport/outbox records. Lower
HTTP credential TTL affects new issuance; it does not silently revoke otherwise
valid existing credentials.

The Operations tab displays usage, resources, and the policy revision and lets
authorized operators submit live updates. Setup issues and revokes device-only
HTTP credentials. No pricing, plan names, invoices, or billing decisions are
implemented in Devices.

Defaults: 10,000 devices; 128 KiB data/command arguments; 1,024 state keys and
1 MiB current-state JSON per device; 10,000 pending commands per scope and 100 per
device; 100 concurrent commands per scope and one per device; 10,000 history rows
and 64 MiB data and metadata JSON per device; 50,000 pending data-event notifications.
Admission is serialized with lifecycle/ingestion/command changes. Capacity
exhaustion rejects new work (HTTP 429); it does not silently evict another
device's data. Increase policy limits or wait for configured retention pruning.
Pending data receipts are not issued for rejected work.

Data/dedup history defaults to 168 hours; command, completed-operation
idempotency, and delivered-event history to 90 days; usage history to 365 days.
Pending lifecycle/command work and undelivered events are not retention-pruned.
Usage and event rows survive device deletion until their retention cutoff.
Completed operation secrets have the shorter recovery window described above.
Hourly pruning preserves current state. MQTT has separate transport limits and
retained-message policy; Devices limits cannot override them.

## Upgrade from 0.2.x

Upgrade MQTT to at least 0.6.0 first and back up both app databases plus the
Devices recovery key. MQTT 0.5+ requires valid TLS configuration, or explicit
`transport_mode: local` with a restricted private/loopback endpoint. There is no
silent plaintext fallback. The Devices migration is additive: current devices,
credentials, history, and commands remain. A stable app-owned MQTT consumer is
created from the earliest retained broker history; original v2 logical IDs avoid
repeating already processed data. Pre-0.6 lost broker messages cannot be restored.
Do not change the Devices app installation identity to upgrade; consumer
ownership is bound to it. Review old consumer cursors before deleting an install.

Set `APTEVA_MQTT_TLS=true` and use the advertised host/port for TLS Python clients;
`APTEVA_MQTT_CA_FILE` supplies a private issuer CA. Defaults are TLS and port 8883,
with hostname and chain verification enabled. For explicit local MQTT, set
`APTEVA_MQTT_TLS=false` and the returned local port. Arduino examples use a CA
certificate and WiFiClientSecure for TLS; copy the enrollment transport mode and
port. No example uses an insecure certificate-verification bypass.

Policy changes through the platform installation config endpoint still restart
the sidecar. Configure the trusted controller/operator allowlist there once,
then use the live policy endpoint for immediate plan changes.

## Upgrade from 0.1.1

Back up the database and review MQTT 0.4's migration instructions first. Existing
Devices rows/data are preserved; legacy telemetry becomes queryable through the
new history API without manufacturing historical usage charges. Legacy Devices
are disabled as `requires_enrollment`, with scope-qualified usernames/topics.

Update firmware with the exact scoped topics and qualified username, review the
MQTT-migrated credential ACL, rotate if needed, then explicitly enable the device.
Existing MQTT migration disables credentials and can quarantine ambiguous data;
Devices never enables or reassigns them automatically. New enrollments use the
private operation credential identity. Back up the new recovery-key file after
first mount. Rollback requires the matching pre-upgrade database backup.

## Examples and verification

- `examples/python/generic_device.py`: generic v2 data, persisted retry queue,
  application receipts, schema-advertised echo operation, and durable commands.
- `examples/python/https_device.py`: HTTPS-only sensor with a device-specific
  write token, verified TLS, persisted submissions, and receipt-driven retries.
- `examples/python/apteva_device.py`: native Raspberry Pi/Linux GPIO adapter,
  using the same durable command runtime.
- `examples/arduino/AptevaDevices.ino`: native ESP32/ESP8266 GPIO, LittleFS command
  journal, absolute expiry, and cached responses. Requires ArduinoJson 6.x,
  PubSubClient, LittleFS, and a synchronized NTP clock. A full/corrupt journal
  fails closed. Its telemetry remains legacy v1 (no application receipt queue).

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
python3 -m unittest discover -s examples/python -p 'test_*.py'
# From the apps repo root:
bun run scripts/build-panels.ts --app devices
```

The sidecar integration test builds real current MQTT and Devices sidecars, checks both verified TLS and explicit local MQTT, and tests HTTPS ingestion, policy updates, and usage summaries. It also checks
scoped enrollment and HTTP isolation, connects a real MQTT client, verifies
data receipts/deduplication, command completion, and deletion replay. Tests also
cover database/remote-call failures, process restart, v0.1 migration, encrypted
secret recovery, outbox delivery failure, retention, and concurrent limits.
The ESP8266 toolchain distributed for macOS is x86_64 and was unavailable on the ARM validation host. The changed ArduinoJson expressions were syntax checked with ArduinoJson 6.21.6; a full board build and physical Arduino/Raspberry Pi behavior still require validation on the target toolchain/hardware.
MQTT 0.6+ supplies TLS and durable broker/session recovery. Devices acknowledges
durable consumer batches after local commit; downstream event subscribers and
physical hardware still need their own idempotency and recovery contracts.
