# MQTT Broker

Version 0.4.0 introduces ownership isolation and explicit credentials. All
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

## Delivery and transport status

This stage implements isolation and credentials. It does not add TLS or
certificate renewal, durable message acceptance, durable MQTT sessions,
acknowledged app consumption/replay, or persistent usage quotas. The existing
transport is plaintext TCP, event/audit queues are best effort, and the existing
publish rate limiter can still acknowledge discarded messages. These behaviors
must change in the subsequent transport and durability stages before claiming
the full proposal's security and delivery guarantees.

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
