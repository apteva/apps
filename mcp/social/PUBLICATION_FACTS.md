# Publication facts and record reconciliation

Social 0.20.5 keeps publication URLs and timestamps account-scoped across native
and provider-backed channels. This is Social-only code: optional Editorial and
Content Catalog integrations use their existing revision-checked tools.

## URL handling

Publication links resolve relative paths against a fixed platform base. For
example, Facebook `/reel/123/?x=a%2Fb` becomes
`https://www.facebook.com/reel/123/?x=a%2Fb`. Valid absolute HTTP(S) links and
meaningful query parameters are preserved. Credentials, user-info, unsafe
schemes, malformed links, unexpected hosts and nonstandard ports are rejected.
No native ID is turned into an invented permalink. Missing links remain unknown.
Asset delivery URLs and YouTube Studio setup links use separate contracts.

This policy is applied at native/provider publication, history/inspection,
metrics, report presentation and historical-repair boundaries. Trusted hosts
are explicit per platform, so a response cannot select an arbitrary base.
Existing stored links are normalized for presentation; durable repair is an
explicit, revision-checked operation rather than a publishing retry.

## Time meanings

Per-target `publication_facts` exposes nullable values:

- `submitted_at`: actual local dispatch time for the current delivery attempt.
- `provider_accepted_at`: an explicit upstream acceptance timestamp, if exposed.
  A successful HTTP response does not date provider acceptance by itself.
- `delivery_recorded_at`: local delivery/status recording time, not publication.
- `native_published_at`: authoritative account-bound native publication time.
- `native_uploaded_at`: a native upload time whose meaning is not publication.
- `provider_reported_published_at`: a provider-indexed date, separately qualified.
- `observed_at`: observation time for the native fact correction.
- `legacy_published_at`: prior ambiguous timestamp retained as evidence.

`published_at` in post/target responses now means native publication. Unknown
values remain absent/empty in the compatibility field and null in the facts
object. Parent dates aggregate known native target dates only when every
published destination has one. Each destination retains its independent time;
post rankings use the destination's time rather than the parent's date.

Completion, status polling, scheduled dates and audience confirmation never
supply a missing native publication timestamp. Native timestamps normalize to
UTC. Processing/future timestamps do not establish completed publication.
YouTube private/unlisted `snippet.publishedAt` is recorded as upload time. It
cannot date membership availability or a later audience change. Membership
confirmation remains an operator attestation with its own actor/time. It does
not supply a membership publication time or change independent Catalog evidence.

The migration retains previous values verbatim in legacy columns and records a
legacy history snapshot. It moves known managed delivery-recording dates into
their own field, without declaring ambiguous old values native publication.
Native history metadata and last-known fields also expose their time meanings.
Post-report caches use a new identity so old parent/local date interpretations
are refreshed; metric observations and historical evidence are retained.

## Repair the same existing records

`post_publication_reconcile` takes `post_id`, `target_id` and
`social_account_id`. It never uploads, retries, schedules or creates assets.

1. Omit `apply` for a dry run. Set `refresh_native=true` to inspect the exact
   native ID through the account's existing connection. Unsupported, missing or
   mismatched native evidence cannot supply a publication time.
2. Review `before`, `after`, evidence, `expected_post_revision` and
   `expected_publication_revision`.
3. Call with `apply=true` and those two revisions. Link normalization and valid
   native-time corrections append a before/after audit record atomically. Native
   IDs, status, approvals, audiences and publishing history remain unchanged.
4. Optionally supply `records:[{app,record_id,expected_revision}]` for existing
   `editorial` or `content-catalog` records. Bind these apps only when needed.

Catalog matching requires the exact platform, native post ID and stable
`social:<Social install ID>:account:<Social account ID>` reference. A legacy
account reference needs mapping review, rather than being silently reassigned.
Editorial matching requires the existing Social publisher link and exactly one
matching target/account/native-ID snapshot. A scalar release URL/date is updated
only when the channel identifies one matching destination; multicast target
facts stay independent. Unspecified result keys and sibling targets survive.

The existing Catalog post ID and asset membership are reused. No attach, import,
asset-create or publishing tool is called. Status, audience, confidence and
independent dates are preserved; a valid native publication date can correct the
same record's date with its previous value and correction evidence retained in
history. An unknown/private upload time never replaces a known membership date.

Remote record updates have their own durable pending/complete receipts. If a
response is lost, resume using the returned `correction_id` with the same
post/target/account and `apply=true`. Read-back markers and remote revisions make
successful writes idempotent. A conflict, changed Social account/content binding
or changed facts revision blocks stale propagation. Reload the same records and
prepare a new correction; never repost to repair a record.

## Delivery history and expired uploads

The durable delivery ledger retains each attempt's identity, submission,
acceptance, recording and publication facts independently of the polling ring.
`post_delivery_inspect` returns `delivery_recording_history`, with
`history_limit` (1–100) and `history_after_id`. Follow `next_after_id` until null.
These reads do not initialize or retry a publication.

Expired TikTok recovery additionally binds its receipt to the original Social
account, connection, approved content/options/revision and source checksum. A
changed source, account or resumable sender blocks replacement. Local unknown
acknowledgement values stay unknown. Only complete application-owned historical
receipts can be explicitly reviewed; missing initialization or sender evidence
requires investigation and remains blocked. See [TIKTOK_RECOVERY.md](TIKTOK_RECOVERY.md).

## Deployment verification

This release does not deploy production, invoke target recovery, or modify
existing Editorial/Catalog production records.

After an operator deploys it:

1. Inspect the target read-only using `post_delivery_inspect` and its Social
   account ID. Confirm current operation, provider progress and complete history.
2. Use `post_delivery_revalidate(review_expired_upload=true)` to prepare an
   expiring decision. This revalidates source/privacy/evidence but never retires
   the operation or publishes.
3. Execute any replacement only as a separate confirmed
   `post_delivery_recover(recovery_id,confirm=true)` action for that target.
4. Inspect native identity and time meaning; preview publication-record repair.
5. Apply record-only corrections with current Social/Editorial/Catalog revisions.
   A record-write failure never authorizes reposting successful destinations.

Regression tests cover normalization/rejection, queries, unknown dates, delayed
publication, timezone conversion, scheduled content, multiple destinations,
private membership staging/confirmation, migration history, concurrent/replayed
recovery, lost initialization/record responses, restart, changed bindings and
late old-operation callbacks. Provider and linked-app writes use test fixtures;
no real publication is required for these checks.
