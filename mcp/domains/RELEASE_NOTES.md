# Domains v0.6.0 release notes

Based on `domains/v0.5.2` (`78c844b486eb32c7d56f01d0e05838b511d0ee5d`). This release includes the audit fixes and the matching Porkbun integration catalog contract. Refresh the integrations catalog before using paid registration.

## Audit coverage

| Findings | Implemented changes |
| --- | --- |
| 1 — wrong-domain UI writes | Reset panel state on project/install changes, key record panes by domain/account, discard stale responses, and submit expected connection and record snapshots. Browser tests exercise delayed replies and purchase scope changes. |
| 2–3 — Spaceship identity/replacement | Hash complete record identity, preserve TXT case and priority, reject duplicate exact selectors, replace the old value for implicit and explicit updates, and verify final state. TTL-only updates avoid deletion. |
| 4 — Namecheap deletion | Validate the requested ID inside the requested name/type set before replacing the zone. |
| 5–7 — account selection | Fail closed on inventory/project/binding failures, persist unmanaged state, preserve existing pins on re-add, and expose explicit metadata/connection updates. |
| 8 — purchase replay | Persist immutable successful results; replay cannot repin or resurrect removed inventory. |
| 9 — TXT equality | Preserve exact TXT content; normalize only record types whose data permits it. |
| 10 — registration contract | Correct the app and integration catalog together: integer USD cents, required terms, registry minimum term, premium rejection, exact dry-run validation, and a header-only idempotency key. Unsupported coupon/renewal options are rejected explicitly. |
| 11 — response validation | Require Porkbun success and expected purchase fields, explicit DNS arrays, Namecheap host/result identity and IsSuccess, and matching IONOS zone details. Handle both documented `Host` and production-style `host` XML. |
| 12 — global REST scope | Preserve the resolved project through HTTP tool dispatch and record listing. |
| 13 — priority | Preserve explicit zero, split Porkbun SRV priority correctly, and include priority in JSON. Spaceship SRV now uses separate service/protocol fields and reconstructs the DNS owner on reads. |
| 14 — appending records | Add `mode:create` and `mode:ensure`; retain legacy upsert behavior. Create refuses duplicates; ensure preserves an existing value and its TTL. |
| 15 — capability/validation | Return write/delete capabilities and TTL bounds; validate numeric arguments, addresses, owner length, wildcard placement, CAA and MX/SRV fields. Hide unsupported editing actions. Accept provider-native Namecheap types for deletion. |
| 16 — apex filtering | Distinguish omitted name from an explicit apex filter. |

## Reliability, performance, and maintenance

- Persist uncertain purchase outcomes. Recovery requires explicit reuse of the same intent/key within a conservative 23-hour window. SQL claims and triggers reject concurrent or conflicting purchases. Ownership inspection never assumes that ownership proves a particular charge.
- Persist Spaceship recovery information before deleting anything. Report rollback failures and reconcile by reading provider state. Block further mutations while the domain has an unresolved recovery. Batch deletes at 500 records and bound pagination.
- Read Namecheap again immediately before replacement, compare the full snapshot independently of ordering, preserve CAA fields and mail-routing mode, and skip unchanged writes. When the provider omits mail mode, require the operator to select the current setting; the UI explains why. Never infer that forwarding/private-email zones use custom MX. Reject unsupported or incomplete zone data rather than silently dropping it.
- Bound and cancel lock waits; remove unused lock entries. Locks cover the domain across account connections within the process.
- Fetch compatible connections in one list call; read binding IDs without resolving every bound account. Cache IONOS zone IDs for five minutes in a bounded cache, invalidate on failed zone reads, and always read fresh records for writes.
- Add searchable, paginated inventory/record tables; display account/unmanaged state, disabled records and warnings. Add metadata editing and Porkbun expiry refresh.
- Trap and restore confirmation-dialog focus, reset cancelled edits, show the actual purchase account/term/price/privacy/renewal/expiry, and expose pending-purchase and DNS-recovery status.
- Split the backend into inventory, database, HTTP, registration, validation, and provider modules. The entry file is now 208 lines. Add reproducible Bun dependencies, UI tests, type checking and a panel-only build command.
- Pin app-sdk v0.75.0 after checking tag ancestry (including atomic migrations), require Go 1.26.6, and update golang.org/x/sys to v0.44.0. Tests use `GOWORK=off` so they verify the released SDK dependency.

## Validation

- 110 Go test functions, including the 19 original audit regressions, plus stateful rollback/recovery, concurrent purchase, immutable replay, migration, TTL, SRV, mail-mode, expiry, and cache checks.
- Full Go suite passes with race detection; statement coverage is 71.2%. `go vet` and the Go build pass.
- Five headless Chrome browser scenarios; strict UI TypeScript checking; regenerated production panel and source map.
- Two integration contract tests (16 assertions) exercise the actual HTTP executor against a local server; the integrations TypeScript build passes.
- `govulncheck` reports no known vulnerabilities in source dependencies or the compiled binary. `bun audit` reports none for the app's development dependencies.
- Porkbun's credential-free mock registration endpoint was checked against the documented result shape. No paid registrations, live DNS changes, or deployment were performed. An authenticated provider sandbox transaction was not run.

Run from `mcp/domains`:

```sh
GOWORK=off go test -race -cover ./...
GOWORK=off go vet ./...
GOWORK=off go build -o /tmp/domains-check .
bun install --frozen-lockfile
bun run typecheck
bun run build
bun run test:ui
bun audit
```

The browser tests use an installed Chrome by default. `CHROME_CHANNEL`, `PLAYWRIGHT_MODULE`, and `REACT_MODULES` can override the local browser/dependency locations. The paired catalog tests run with `bun test test/domains-provider-contracts.test.ts` in the integrations worktree.

## Deployment considerations

Deploy the app and corrected Porkbun integration catalog together. Migration 004 marks legacy null pins unmanaged and expires old unvalidated quotes; operators must explicitly select an account for those inventory rows. Back up the app database before a release migration.

Namecheap does not expose conditional whole-zone writes, so another installation or external operator can still change a zone between the final read and write. Spaceship value replacement also remains non-atomic at the provider. These API limits are mitigated by fresh snapshot checks, serialization, durable recovery, verification, and explicit conflict reporting; they cannot be made globally atomic by this sidecar alone.
