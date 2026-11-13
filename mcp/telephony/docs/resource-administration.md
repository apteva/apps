# Browser resource administration

Telephony 0.19.0 adds an app-only provisioning API for browser destinations and
ordinary call grants. Accounts and business workflows stay in the calling app.
Principals are opaque issuer, installation, subject and organization references.
Runtime authentication uses an approved online provider.

## Operator setup

Use the existing authenticated `PUT /access/policy` boundary. Keep the approved
`providers` configuration and add a `delegations` array. Each entry has this
shape. Replace sample installation and organization references with actual ones.

```json
{
  "app_install_id": 91,
  "app_name": "functions",
  "permissions": [
    "telephony.destinations.read", "telephony.destinations.write",
    "telephony.grants.read", "telephony.grants.write"
  ],
  "destination_prefixes": ["desk-"],
  "authorities": [{
    "issuer_app": "auth", "issuer_install_id": "11",
    "organization_id": "org-1", "subject_type": "user"
  }],
  "actions": ["call.read", "call.answer", "call.attach", "call.hangup"],
  "outbound_numbers": [],
  "manage_others": false
}
```

`destination_ids` adds exact IDs alongside prefixes. Only browser destinations
and user principals can be provisioned. Supervision actions cannot be delegated.
Missing rules deny service administration. Rules require both the calling app
name and its verified installation ID in the current project. A delegated
subject on a Functions call does not erase this service identity.

Omitted or null `delegations` preserve the existing rules during legacy policy
writes. An explicit empty array removes them. Bound services cannot write this
policy or use legacy routing administration to bypass its resource bounds.

## Tools and contracts

The calling app needs `platform.apps.call`, a Telephony dependency or binding,
the appropriate provided permission, and operator delegation.

| Tool | Purpose |
| --- | --- |
| `telephony_destinations_get` | Read one permitted browser destination |
| `telephony_destinations_upsert` | Create or update a destination |
| `telephony_destinations_list` | List with optional `kind`, `enabled`, `limit`, `cursor` |
| `telephony_grants_get` | Read a grant, including a tombstone |
| `telephony_grants_list` | Filter by `principal`, `resource`, `include_deleted`, `limit`, `cursor` |
| `telephony_grants_upsert` | Create, update or explicitly restore a grant |
| `telephony_grants_delete` | Disable and tombstone a grant |

New tools have `exposure: app_only`. Existing operator and authenticated agent
contracts are preserved. The existing destination list includes a `destinations`
alias for scoped service callers as well as the new result envelope.

Create a destination with this `telephony_destinations_upsert` request.

```json
{
  "id": "desk-42", "name": "Desk 42", "kind": "browser",
  "config": {"capacity": {
    "identity": {
      "issuer_app": "auth", "issuer_install_id": "11",
      "subject_type": "user", "subject_id": "42", "organization_id": "org-1"
    },
    "concurrent_call_limit": 1
  }},
  "enabled": true, "expected_revision": null,
  "idempotency_key": "create-desk-42"
}
```

Then call `telephony_grants_upsert` with the same principal.

```json
{
  "id": "grant-desk-42",
  "principal": {
    "issuer_app": "auth", "issuer_install_id": "11",
    "subject_type": "user", "subject_id": "42", "organization_id": "org-1"
  },
  "resource": {"kind": "destination", "id": "desk-42"},
  "actions": ["call.read", "call.answer", "call.attach", "call.hangup"],
  "enabled": true, "expected_revision": null,
  "idempotency_key": "grant-desk-42"
}
```

Ordinary destination actions are `call.read`, `call.dial`, `call.answer`,
`call.attach`, `call.hangup`, `call.hold`, and `call.recording.control`.
An `outbound_number` resource uses an owned E.164 caller ID and permits only
`call.dial`. Calling requires both destination and number permission. A user
with several dial destinations selects `destination_id` when placing the call.
A single eligible destination is selected automatically. The normal outbound
resolver still rechecks ownership and admission.

IDs and idempotency keys are nonempty strings of at most 128 bytes. Destination
IDs also follow existing routing validation. Revisions are opaque strings.
`expected_revision: null` means create only. Updates and deletes need the
current revision. Grant principal and resource references are immutable.

A mutation commits the resource, revision, receipt and audit together. Retry the
same normalized request and key after an uncertain response to recover its
original result with `replayed: true`. Reusing a key for another request returns
`idempotency_conflict`. Current delegation is checked before receipt replay.
Operator destination edits advance the same revision as service updates.

New tools return `{"ok":true,"data":...,"replayed":false}` or
`{"ok":false,"error":{"code":...,"message":...,"retryable":false}}`.
Callers must also handle SDK/MCP authorization errors. Error codes include
`invalid_argument`, `not_found`, `forbidden`, `revision_conflict`,
`idempotency_conflict`, `invalid_reference`, `unsupported_action`,
`unsupported_principal_type`, `resource_in_use`, and `storage_unavailable`.
Storage errors are retryable. Destination responses whitelist fields and omit
credentials, media URLs and session tokens.

Lists default to 50 records, cap at 100 and order by stable ID. Signed cursors
bind the caller, project, delegation, operation and filters.

## Effective access

Legacy memberships and independent enabled grants contribute access together.
An explicitly disabled legacy user overrides independent grants. Deleting one
grant removes only that grant's contribution. Authentication scopes, resource
actions, call ownership, capacity and session rules must all allow access.
Grant changes advance the permission epoch before their response. Cached
attachment retries reauthorize and cannot restore revoked actions. Destination
disabling stops new browser admission while preserving carrier calls and
historical ownership.

## Local verification

Focused tests use `GOWORK=off go test -race -run '^TestPhoneResourceAdmin' .`.
The real Auth/Chromium fixture provisions its destination and grant through the
SDK tool transport before testing login, answer, media, reconnect and logout.
Run it with `GOWORK=off go test -tags integration
-run '^TestTier2HeadlessBrowser$/^application-user$' .`.

The opt-in benchmark uses 32 measured operations and eight concurrent callers
for separate first and warm read/write batches. Run with
`GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=4GiB RUN_TELEPHONY_RESOURCE_BENCH=1 go test
-run '^TestPhoneResourceAdminPerformance$' -v .`. It reports latency p50/p95,
errors, SQLite pool wait, mutation transaction queue/residence time and memory.
Read operations have no write transaction. These local process measurements do
not substitute for a shared 2 vCPU / 4 GB host or Environments runtime test.
