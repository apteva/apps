# Domains

DNS, domain inventory, and registration through Porkbun or Spaceship for Apteva projects. See [the release notes](RELEASE_NOTES.md) for release requirements.

- Domains are pinned to a connection or explicitly unmanaged. `domain_update` changes the pin and clears notes. Unknown domains retain default-provider compatibility for existing inter-app callers; a failed inventory read never falls back to a default.
- `domain_records_set` supports `mode:create` (append), `mode:ensure` (preserve an existing value, including TTL), and `mode:upsert` (the compatibility default). `record_id` targets exactly one record. UI mutations include optimistic connection/record snapshots.
- `domain_records_list` returns provider capabilities. Unsupported write types remain readable, with exact deletion available where the adapter can preserve the provider's identity. Internationalized input must already be ASCII/Punycode.
- For Namecheap zones whose response omits mail routing, supply the current `namecheap_email_type`: `MX` (custom), `MXE`, `FWD` (forwarding), or `OX` (Private Email). The panel requires this selection when needed. Whole-zone writes preserve that choice and CAA fields.
- Registration requires `domain_registration_prepare`, review of the returned quote and explicit purchase confirmation, then `domain_register`. `domain_registration_status` inspects pending intents, cancels unsubmitted ones, or reads registrar ownership. Resume uncertain purchases only with the original token; no automatic fresh purchase follows a timeout.
- `domain_dns_recovery` lists interrupted Spaceship replacements and can reconcile one by reading provider state. Unresolved recoveries block further DNS mutations for the domain. `domain_sync` refreshes Porkbun registration expiry.

## Checks

```sh
GOWORK=off go test -race -cover ./...
GOWORK=off go vet ./...
bun install --frozen-lockfile
bun run typecheck
bun run build
bun run test:ui
```

`bun run build` rebuilds only this app's panel. Browser tests use headless Chrome and mock all app/provider requests. The Go tests use a temporary SQLite database and stub providers.

## Spaceship purchases

Bind a Spaceship connection to `registrar_provider` (or the default DNS role). Grant `domains:read`, `domains:billing`, `contacts:read`, and `asyncoperations:read`. Use `save_contact` in the integration to create a contact, or obtain existing contact IDs from `get_domain_info`. Restricted TLDs can require contact attributes, supplied through the existing integration contact-attribute tools.

The purchase flow is `domain_registration_prepare` followed by explicit confirmation and `domain_register`. Preparation accepts `contacts` (registrant required, admin/tech/billing and attribute IDs optional), `years` (1–10), renewal/privacy settings, and `accept_account_rate:true`. The UI can use one existing contact ID for all roles, with optional attribute IDs.

Spaceship's documented registration endpoint bills the account's current rates. It has **no dry run, standard-domain price quote, maximum-price field, or documented idempotency key**. The confirmation therefore displays account-rate consent rather than a fabricated quote. Premium registrations are rejected. Check current pricing in Spaceship before confirming.

A `202` response means accepted, not registered. The app stores the exact approved request and async operation ID in SQLite. `domain_register` on a submitted token or `domain_registration_status` polls the stored operation; it never sends the registration POST again. Only successful operation status and matching account ownership add the domain to inventory. Failed operations remain in the audit record. A timeout or missing operation header leaves an unknown outcome and blocks resubmission; inspect the account or contact provider support.

Deploy the paired Spaceship catalog and integration executor change **before** enabling purchases. That update preserves `spaceship-async-operationid` as `operation_id` in the JSON result of empty HTTP 202 responses, without forwarding unrelated headers. Migration 006 retains the existing purchase history and adds immutable request options, billing mode, and operation ID.
