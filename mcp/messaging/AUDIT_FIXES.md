# Messaging 0.13.47 — audit fixes

Base: Messaging **0.13.46**, apps commit `ccc8145a72fcd499b94ced8efac31de0f4769518`.
Release: **Messaging 0.13.47**.

## Primary findings

| Audit # | Implemented change |
|---|---|
| 1 | WhatsApp reconciliation uses its own sender resource, status model, and pagination. |
| 2 | SES/Twilio inventory is fully parsed and paginated before reconciliation; malformed responses and continuation tokens fail without deleting senders. |
| 3 | Compose preserves edits when sender data refreshes. |
| 4 | Compose checks returned delivery state; provider failures preserve the draft and display their reason. Uncertain retries reuse an idempotency key. |
| 5 | Inbound messages and recoverable jobs commit together before acknowledgement. A worker retries attachments, STOP suppression, and dispatch with leases, backoff, and a bounded retry count. |
| 6 | Routing uses SES envelope recipients, including Bcc; RFC headers remain display/threading data. |
| 7 | Provider delivery IDs deduplicate inbound messages; reused RFC Message-ID values no longer discard different deliveries. |
| 8 | Sender deletion requires local ownership, respects other projects, blocks domains with active children, and uses channel-specific provider updates. Inherited mailboxes never delete standalone SES identities. |
| 9 | SES reads the exact receipt rule and updates it in place, preserving actions, recipients, TLS and enabled settings. Malformed state and conflicting active rule sets are rejected. |
| 10 | Unhealthy DKIM adoption no longer deletes/recreates identities. |
| 11 | SES configuration sets are installation/connection scoped, persisted, and used by raw and simple sends. |
| 12 | Attachment-only email works through MIME generation and compose. |
| 13 | Missing and cross-project message IDs return not-found instead of dereferencing nil. |
| 14 | Generic inbound dispatch uses the actual MCP tool name; CRM compatibility remains. |
| 15 | Refresh preserves onboarding state, disables missing parent domains and their inherited mailboxes, and advances sync timestamps. |
| 16 | Explicitly deleted senders are rejected in both installation scopes. |
| 17 | Status transitions read authoritative state inside the event transaction, use deterministic terminal precedence and monotonic timestamps, and persist individual recipient outcomes. |
| 18 | Early callbacks enter a durable unmatched-event inbox and replay after correlation. Database lookup/save failures are surfaced. |
| 19 | Email previews deny network resources through CSP until image opt-in; switching messages resets consent. |
| 20 | Custom headers select raw MIME so they reach the provider. |
| 21 | Canonical sender addresses and normalized recipients are stored and indexed for exact filtering. |
| 22 | Live updates are coalesced, visible data refreshes selectively, quota is cached, background sender refreshes are coalesced, and stale list/detail responses are ignored. Summary lists omit large content; trigram full-text search narrows literal matching. |

## Additional changes

- Authorized storage-backed attachment downloads, visible processing errors, and manual retry actions.
- Reply-To addressing, quoted display names, and channel-specific reply sender selection.
- Channel, priority, and MCP tool names in route editing.
- Sender creation applies `set_default`; compose honors the default.
- Suppression pagination; template pagination and saved email/SMS template selection.
- New callback URLs use nonsecret installation routing IDs. Legacy metadata URLs are redacted; provider signature validation remains mandatory.
- Virus-failed inbound email is quarantined; verdicts accompany dispatched messages.
- DNS setup preserves existing SPF/DMARC/MX and bucket policy statements. Provider regions must be known and agree before bootstrap; MX publication follows inbound setup. Missing DNS inventory produces an actionable setup result instead of an overwrite.
- Manifest/schema wording matches explicit channels and optional default `from`.
- SDK dependency advanced from v0.67.0 to descendant tag v0.74.1 after checking repository topology, including authenticated signed-route handling.

## Validation

| Check | Final result |
|---|---|
| Messaging Go suite | PASS — 223 test functions (including the performance fixture) |
| Messaging race suite | PASS — 222 test functions; 65.3% statement coverage; 92.4 seconds |
| `go vet ./...` | PASS |
| Go binary build | PASS |
| UI behavior tests | PASS — 10 tests |
| UI TypeScript check | PASS |
| Production UI bundle | PASS — 74,406 bytes; sourcemap rebuilt |
| Integrations suite | PASS — 301 tests across 75 files |
| Integrations TypeScript build | PASS |
| Changed catalog/server mirrors | PASS — byte-identical |
| Diff whitespace checks | PASS in all three worktrees |
| Live AWS smoke tests | 3 skipped because credentials were not configured |

The initial three-minute race limit expired while tests were progressing. The completed final run used a ten-minute limit and reported no races. The release is revalidated against the published SDK pin before publication.

Performance fixture: 100 messages with approximately 50 KB of text/HTML each. A 50-row response decreased from **2,708,502 bytes to 30,802 bytes (98.9%)**. A unique-text indexed query took **3.1 ms** locally. This is an in-memory regression fixture, not a production load benchmark. The performance fixture runs without race instrumentation; correctness and worker tests also run with the race detector.

## Integration and rollout notes

The companion integrations **v0.36.2** release and server catalog mirror update add SES `describe_receipt_rule` / `update_receipt_rule` and Twilio continuation parameters. The source and server-mirrored JSON are byte-identical. Refresh the integrations catalog before upgrading. The server also embeds a catalog at build time, so the matching mirror update is included in future server builds. The UI bundle and sourcemap have been rebuilt.

Migrations 012–013 add durable processing/event inboxes, recipient outcomes, canonical address indexes, and the full-text index. Previously unfinished inbound rows can resume routing, but raw attachment bytes discarded by older releases cannot be reconstructed by a migration.

Inbound dispatch is at least once. A stable `idempotency_key` is supplied to target tools; those tools must honor it to make their own side effects exactly once. Completed jobs clear stored source payloads. Exhausted jobs remain inspectable and can be retried manually.

Existing installations retain their legacy SES configuration-set name until setup refresh establishes the scoped configuration. Existing provider subscriptions are migrated through onboarding; these local changes do not modify live AWS/Twilio resources.

The three live AWS smoke tests were invoked and skipped because AWS credentials were absent. No live SES/Twilio delivery or browser-network test was performed.
