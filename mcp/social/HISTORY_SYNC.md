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
   receipt without advancing again. Provider calls have bounded read timeouts;
   the loop also checks its time budget between pages.
4. Check `coverage.complete`, `coverage.scope`, `pagination_exhausted`,
   limitations and observation time. A completed traversal uses two bounded head
   reconciliation pages. This is best-effort provider pagination, not an atomic
   provider snapshot or a guarantee that deleted/inaccessible lifetime history
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
continuation cursor. Provider cursors, signed pagination URLs and credentials are
not returned. Counts reconcile through a transactionally stored per-run identity
ledger; repeated observations may put an ID in more than one activity category.

Observations are upserted by project + Social account + native post ID. Existing
managed bodies, approvals/revisions, target status, accepted operations, provider
history and delivery evidence are preserved. Newly discovered history is read-only
local history and creates no Jobs, schedules, retries, uploads or provider writes.
Native uploads that are processing/unknown are not recorded as confirmed local
publication. Provider local-deletion tombstones are honored and limit completeness.

Current metadata contains nullable native URL, title/body, publication time,
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

The fixture reconciles 299 native YouTube videos with 28 managed records,
overlapping pages, repeat runs and accepted-operation preservation. Other tests
cover literal search beyond page one, account/project/owner/cursor isolation,
failed-page resume, fixed page sizes, provider cursor cycles, unknown versus zero
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
