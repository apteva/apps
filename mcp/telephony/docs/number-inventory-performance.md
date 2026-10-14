# Connected number inventory

`/numbers/connected`, `telephony_numbers_connected` and the authorized headless
`/softphone/numbers` endpoint share carrier reads. Call placement retains its
current binding, credentials, ownership, outbound readiness and admission checks.
Inventory is a selection hint; it never authorizes placing a call.

## Read path

- Resolve current authorized bindings and credentials on every request. Reuse
  the connection credentials and Telnyx signing-key validation within that
  request. Credentials are not stored in the carrier response cache.
- Read current local routes, then fingerprint installation, project, connection,
  credential fields, routing configuration and current authorized bindings.
  Only the digest is used internally; it is not returned or logged.
- Deduplicate application reads by connection and application ID. Both webhook
  verification and outbound readiness consume the same response. Independent
  Telnyx application reads use four workers per account refresh. A shared
  installation limiter caps all simultaneous inventory carrier reads at 32.
  Different accounts retain independent loading and partial-result handling.
- All inventory carrier reads, including Telnyx/Plivo webhook checks, use the
  existing eight-second inventory deadline. Shared readers retain the first
  reader's deadline; canceling one reader does not interrupt other readers.
  Canceling the last reader cancels the underlying fetch. Clients without SDK
  request-context support retain sequential compatibility; their underlying
  calls cannot be forcibly canceled, but their late results do not refill a
  canceled cache entry.
- Cache successful raw carrier responses for 25 seconds, with at most 1,024
  entries and 16 MiB of response data. Share identical in-flight reads. Failed
  checks do not become healthy and a failed fresh check evicts an older success.
  Parsing and permission filtering use independent data for every response.
- Local routes, agent names, number/provider controls, draining state and
  caller-ID permissions are recomputed. No permission-filtered final response
  is shared between users.

Number purchases, carrier-route configuration, outbound-profile changes,
route toggles, outbound policy and runtime binding changes clear the cache at
mutation boundaries. Other local route/binding/credential changes also change
its fingerprint. Late completions cannot restore invalidated entries. Changes
made directly in a carrier console are observed within the TTL or by a fresh
check; Telephony cannot receive every external change automatically.

## Explicit verification and age

- Admin HTTP: POST `/numbers/connected` with `{"fresh": true}`.
- MCP: `telephony_numbers_connected` with `{"fresh": true}`.
- Authorized headless HTTP: GET `/softphone/numbers?fresh=true`.
- Headless client: `client.outboundNumbers({ fresh: true })`.
- Numbers and caller-ID panel Refresh buttons request a fresh check.

`verified_at` on each number and successful provider status is the oldest
successful carrier read contributing to the account snapshot, in UTC. It shows
cached data's age; it does not assert healthy status. Readiness/health errors
remain explicit. The Numbers panel displays the check time. Fresh checks still
share identical reads that are already in progress.

## Verification and local benchmark

The test fixture has eleven numbers/applications and ten enabled routes. It
reconstructs the prior carrier-check path and compares every returned number,
route, webhook and outbound-readiness field against the new path. The prior
path issues 23 carrier reads; the new path issues 13 and reads connection
credentials once.

Run:

```sh
GOWORK=off go test . -run TestInventory -count=10
GOWORK=off go test . -run '^$' -bench '^BenchmarkNumberInventory$' -benchtime=3x
```

Apple M1 Pro, simulated 200 ms per carrier response, three measured iterations:

| Path | Time | Carrier reads |
| --- | ---: | ---: |
| Previous carrier checks | 4.633 s | 23 |
| Cold inventory | 1.010 s | 13 |
| Cached inventory | 4.071 ms | 0 |
| Twelve concurrent cold requests (whole group) | 1.041 s | 13 total |

This is about 78% less cold elapsed time and 43% fewer carrier reads. Cached
requests continue to incur current credential/authorization and local database
work. The previous-check baseline excludes final admission/permission response
assembly, while the optimized benchmark executes `connectedNumbers`. These
measurements simulate carrier latency; they are not production measurements or
CPU-profile evidence. Cold allocations were 335 KB/op versus 192 KB for the
previous checks; cached inventory used 250 KB/op. Response decoding and request
isolation intentionally remain per caller.

Tests cover equivalent results, one fetch per application, credential reuse,
failed application isolation, cache failure eviction, invalid signing keys,
four application workers, the 32-read global ceiling, cancellation, independent
shared readers, expiration, late invalidation, scope changes, live permission
filtering, disabled numbers and concurrent fresh refreshes. Existing account,
admission, routing, media and audio suites remain release gates.

## Temporary carrier inventory outages

A temporary REST verification failure (408, 429, 500, 502, 503, 504 or timeout)
may use successful verification evidence from the last 24 hours. This covers
number selection, programmable outbound readiness and SIP trunk verification;
SIP signaling itself still has to succeed independently.

Evidence is install-local in SQLite, scoped to project, carrier binding,
provider, current credential digest and the exact outbound resource/config.
It contains no API keys or SIP passwords. Ownership must come from a full
successful inventory and the number must still be configured locally. A route
or caller ID alone cannot establish proof. A new installation or upgrade must
complete a healthy verification before this fallback becomes available.

Current bindings, credentials, per-number/provider/account admission, drain
state, user identity and grants remain mandatory. A successful inventory that
excludes or disables a number rejects it and replaces prior ownership evidence.
Permanent/unknown verification errors revoke the affected evidence. Fresh
profile disablement also overrides older readiness even if a subsequent
application lookup fails. Failed reads cannot renew evidence; timestamped
revocations prevent older in-flight successes from restoring it. Expired rows
are removed in indexed batches of at most 64 when evidence changes, with no
background poll or media-path work.

During an outage, provider status remains `unavailable` and warnings retain the
failed live check. The top-level inventory is `partial` when it contains eligible
configured fallback numbers, and `unavailable` when no verified fallback is
usable. This keeps existing generic clients working without client-specific
exceptions. Outbound `ready` describes eligibility to attempt placement, while
`outbound_verification.status: "previously_verified"`, UTC
`verified_at`/`expires_at` and `outbound_fallback_allowed` explicitly distinguish
cached evidence from fresh verification. Webhook health remains `unverified`.

Current permissions and admission are enforced again at placement, independently
of the inventory response. A failed inventory request with no valid evidence
stays unavailable. Dial commands themselves are never automatically retried by
this fallback. No CRM or other consuming application's changes are required.
