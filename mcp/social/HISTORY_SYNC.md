# Account history sync and search

Use Social account IDs for history reads, native inspection and publishing.
Integration connection IDs remain inside Social's provider adapters.

## Agent flow

1. Resolve the intended account with `account_list` and its project/profile.
2. Read `account_history_status(social_account_id)`.
3. For an explicit backfill call `account_history_sync` with `mode: "full"`.
   Persist the compact receipt. Follow `continuation_cursor` using the same
   account/project until it is null. `max_pages` defaults to 2 (maximum 5),
   `page_size` defaults to 50 (maximum 50; individual providers can return less).
   Keep page_size fixed when resuming. Replayed step cursors return the latest
   receipt without advancing again. This is the original requested page size;
   Social persists separate effective traversal and reconciliation sizes internally.
   Provider calls have bounded read timeouts;
   the loop also checks its time budget between pages.
4. Check `coverage.complete`, `coverage.scope`, `pagination_exhausted`,
   limitations and observation time. A completed traversal begins a bounded head
   reconciliation. Comparable provider
   totals are retained (for YouTube, the uploads playlist total, not the channel's
   public-only video count). A count mismatch, overlapping pages, changing totals
   or newly discovered IDs extends reconciliation to a second traversal, capped at
   100 pages across the same resumable calls. Reconciliation starts at a different
   supported page size (for example YouTube 50 → 49), with a fresh provider cursor.
   Both sizes, cursor and accumulated IDs persist across bounded calls and retries;
   provider tokens are never reused across sizes. Provider limits are respected,
   including TikTok's 20-item maximum and X's 5-item minimum. Request/page budgets
   are unchanged. This is best-effort provider pagination, not an atomic provider
   snapshot or a guarantee that deleted/inaccessible lifetime history
   exists. Failed, repeated-cursor/page, suppressed or omitted records cannot
   establish complete stored coverage. No automatic sync setting is changed.
5. Search `post_list` with `social_account_id`, `query` and/or exact
   `native_post_id`; use `compact: true` for summaries. Search covers stored
   title/body/original filenames before pagination. Existing profile, status,
   date and offset pagination remain available. Follow next_offset when reading
   all matches. Unknown original filenames are not invented. A filename/text
   match is a duplicate candidate, not proof of identical Catalog asset bytes.
6. Use `post_inspect(social_account_id,native_post_id)` for fresh provider
   metadata. The response declares its verification scope and never claims
   independent public-player/access verification. Unsupported reads are explicit.

## Receipts and persistence

Receipts include status, observation/completion time, imported/updated/unchanged
counts, distinct native IDs observed, pagination/coverage and an opaque Social
continuation cursor. Coverage separates first-traversal identities, reconciliation
identities and their union, duplicate entries, comparable provider totals, changing
totals, reconciliation page budget/exhaustion and any unexplained count gap. A
remaining mismatch, changing total or exhausted reconciliation budget is incomplete.
When no comparable total is exposed, count verification is unknown/false and an
explicit pagination-only limitation is returned. Pre-0.20.3 coverage cursors/receipts must
be refreshed rather than reused as complete evidence. Provider cursors, signed
pagination URLs and credentials are
not returned. Counts reconcile through a transactionally stored per-run identity
ledger; repeated observations may put an ID in more than one activity category.

Observations are upserted by project + Social account + native post ID. Existing
managed bodies, approvals/revisions, target status, accepted operations, provider
history and delivery evidence are preserved. Newly discovered history is read-only
local history and creates no Jobs, schedules, retries, uploads or provider writes.
Native uploads that are processing/unknown are not recorded as confirmed local
publication. Provider local-deletion tombstones are honored and limit completeness.

Current metadata contains nullable returned native URL, title/body, publication time, native upload time and time source,
visibility, audience, processing state, original filename, byte size and duration.
`observed_at` is separate from `published_at`; no observation/playlist-entry time
is substituted for an unknown native publication time. Later omissions remain
unknown in the current observation while last-known fields and their individual
observation times remain available for historical filename search. Private or
unlisted never implies members-only; YouTube membership visibility remains unknown.

History status reports a prior complete traversal and whether it was observed
within 15 minutes, with latest-run errors/limitations preserved. Current is false
for failed latest reads and disconnected accounts. It is not a guarantee that the
provider has not changed since observation. Cursors/owner attestations prevent a
Social account from silently switching channels after reconnection.

## Provider coverage

| Path | Listing | Native readback and limits |
|---|---|---|
| YouTube | Owned uploads playlist + batched video metadata | Verifies the OAuth owner/channel; optional owner file/processing fields may be unknown |
| Facebook | Page posts + after cursor | Exact Page-bound post readback |
| Instagram | Account media + after cursor | Readback requires selected account ownership in provider response |
| X | Authenticated author's paginated timeline | Author-bound post readback; provider plan/retention limits apply |
| TikTok | Cursor-paginated public video history | Owner-scoped Display API readback; private/deleted history is not exposed |
| Pinterest | Account Pins, or explicit legacy board scope, + bookmarks | Accessible Pin readback; board bindings are preserved |
| LinkedIn native | Selected author URN with permitted list access | Fresh per-post inspection is explicitly unsupported by this adapter |
| Zernio (all connected channels) | Paginated provider-indexed published posts | Account/native-ID-bound provider readback; does not prove complete native backfill |
| Other unavailable native paths | Explicitly unsupported | No missing data is interpreted as absence |

Social account responses expose history capabilities. An explicit UI single-account
sync follows a bounded number of batches and stores the opaque cursor for the next
click. The legacy multi-account import action remains a bounded recent import.

## Validation and rollout

The page-boundary fixture reports 300 native videos. Every size-50 traversal
returns the same 298 with overlapping pages; size 49 returns a different 298.
One logical sync reconciles the union to all 300 with zero gap. Interruption,
resume, cursor replay and repeated sync preserve counts, managed records and
accepted operations, without uploads, retries or publishing jobs. Negative
fixtures cover identical omissions at both sizes, changing totals and exhausted
reconciliation budgets. Other fixtures retain the 299-video/28-managed-record
case, account/project/owner isolation, search beyond page one, unknown/zero
metadata, private/unlisted handling and protected HTTP paths.

Opt-in live tests run provider reads through an existing connection into a
local temporary test database; no workers start and no OAuth credentials are
copied. Example: `GOWORK=off SOCIAL_LIVE_YOUTUBE=1 SOCIAL_LIVE_CONNECTION=<id> go test -run '^TestLiveYouTubeHistoryReadOnly$' -v`.

Upgrade Social and verify its running manifest before applying prepared SOP/access
changes. Ensure the agent's Social tool allowlist includes account_history_sync,
account_history_status and post_inspect. Verify each account's required capabilities
before removing direct provider verification/preflight connectors. Preserve unrelated
apps and unsupported-platform/Patreon workflows. Workflow changes are prepared
separately, not automatically applied by this app release.

Since 0.20.5, owner-private/unlisted YouTube snippet timestamps are upload times,
not native publication or membership-availability times. Returned URLs are
normalized against trusted platform hosts; missing permalinks stay unknown.
Native observations remain separate from managed publishing state; use
`post_publication_reconcile` for audited corrections to existing delivery/linked
records. See [PUBLICATION_FACTS.md](PUBLICATION_FACTS.md).
