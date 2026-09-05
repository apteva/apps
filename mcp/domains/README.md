# Domains

DNS, local domain inventory and Porkbun registration for Apteva projects. See [the release notes](RELEASE_NOTES.md) for the v0.5.2 audit changes and release considerations.

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
