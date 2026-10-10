# Content Catalog

## Version 0.6.7: content purpose and hidden provenance

Assets have an explicit `role`: `unspecified`, `main`, `derivative`, or
`intermediate`, independent of their source links, media type, review and
lifecycle. A cleaned master can be Main and keep its archived original as a
source. Existing records default to Unclassified; migration does not infer roles,
classify historical files, or rewrite business records. Optional `output_type`
is a generic lowercase category token, such as reel, screenshot, portrait or
trailer. Categories drive the grouped card counts; unspecified categories show
media kinds instead of guessing from filenames.

The asset modal and bulk editor set purpose/category through
`content_catalog_assets_labels_update`, with required expected revisions for
purpose changes. Updates are transactional, append purpose audit events and
preserve files, source links, hosting and publications. Stale batches fail with
no partial changes. Exact source search, global search, asset lists and session
reads accept `role`, `output_type` and `include_intermediates` (default false).
An explicit `role=intermediate` also enables inspection of that role. Lifecycle
remains independent: seeing intermediates does not expose archived cards.

Default cards and agent selection exclude intermediates. Direct reads expose
them and their provenance; eligibility is false and new hosting/publication
requests reject them. Recorded hosting checks and historical reads remain
available. Descendant and ancestor traversal still follows intermediates and
archived ancestors; only result cards are filtered. This changes Catalog only.
Media is unchanged, and Catalog cannot intercept direct calls to other apps.

**Grouped by main** is the default for browsers without a saved display choice.
It follows project-scoped recorded ancestry through hidden or archived context
nodes to a visible main asset, returning each selected output once. Main assets
stay roots even when they have parents. Context never becomes a card or counts
as an output. Main-only filtering keeps the selected main's outputs accessible.
Grid and search also guard lifecycle before rendering to avoid stale archived
cards after changing scope. Archive/All and intermediate inspection are explicit.

An asset's `ancestors` array contains thin provenance nodes (identity, purpose,
category, lifecycle and source links), batched across the page. These nodes are
context only, not extra content results. The original `sources` are unchanged.

For the reported collection, assign the eight masters Main, the four supporting
frames Intermediate, and the 104 selected outputs Derivative with their output
categories. The result is eight main cards, each with 3 reels, 5 screenshots and
5 portraits. These counts are data-driven; they are not coded into the app.


## Version 0.6.6: exact source search

`content_catalog_search` and HTTP `/search` accept `source_asset_id` (an exact
Catalog asset ID) and optional boolean `include_descendants`, default false.
Direct mode returns assets whose recorded sources include that ID. Descendant
mode follows recorded links through all intermediate assets in the same project.
The source itself is excluded. Each result appears once with its source links;
filenames and descriptions never establish ancestry. Unknown or inaccessible
source IDs fail explicitly. Descendant mode requires a source ID.

These are asset filters: use `entity_type=assets` (or `all`, which returns only
asset matches for this query). Existing brand, session, date, lifecycle, review,
text and publication filters continue to apply to returned assets before
pagination. Intermediate nodes are not restricted by result filters; for
example an image query can traverse a video intermediate, and active crops can
still be inspected when a parent has been archived. Active remains the default
lifecycle for returned assets and their sessions.

### Generic planning workflow

1. Resolve the source's exact Catalog ID; do not identify its images by filename.
2. Search with `source_asset_id`, `kind=image`, and the required review/lifecycle
   filters. Enable descendants when linked crops or other nested outputs are needed.
3. Collect every page: a nonempty `assets.next_cursor` means more matches remain.
   Pass that value as `cursors.assets` with the same filters until no cursor is
   returned. The limit of 100 is a page size, never an inventory count.
4. Exclude published or otherwise unavailable assets using recorded publication
   evidence, then check the planning app's reservations separately. Catalog
   publication filters cannot see reservations stored only in Editorial or
   another planner. Treat related crops of a reserved frame as the same moment.
5. Recheck Catalog eligibility before processing or publishing selected assets.
   Missing lineage or failed searches are uncertainty, not proof of exhaustion.

Example (replace the source ID with the selected source):

```json
{
  "entity_type": "assets",
  "source_asset_id": "<catalog-source-id>",
  "include_descendants": true,
  "kind": "image",
  "review_status": "approved",
  "lifecycle": "active",
  "limit": 100
}
```

This release adds a source lookup index and read-only search behavior. It does
not create source links, scan folders, reserve assets, or change other apps.

## Version 0.6.5: session hosting tool discovery

The read-only hosting list advertises both asset and session queries with an
exclusive choice in its MCP schema, matching the existing handler.

## Version 0.6.4: durable hosting requests and encoding progress

Explicit hosting requests persist while waiting for checksum verification and
resume automatically after eligibility and destination checks. Pending remote
videos refresh automatically, showing encoding progress, provider stage, and
transcoding messages. New uploads use Media's title when available, otherwise
the session title and filename. Existing hosted titles stay unchanged. These
workers only follow recorded hosting work; they never scan folders.

## Version 0.6.3: verified Storage checksums during hosting

Catalog refreshes an asset's checksum from Storage's verified result and
requests exact-file repair when hosting encounters a pending checksum. In v0.6.3, callers
retried hosting after verification; the durable intents described below now
resume automatically, while existing reservations prevent duplicate uploads. Storage checksum-ready events
also refresh the existing Catalog asset record.

Content Catalog coordinates production sessions, Storage files, Media metadata, Gigs, cloud video hosting, and platform posts. Catalog owns stable IDs and relationships. Storage owns file bytes; the publishing platforms own external posts.

## Sessions and files

A brand has an explicit Storage root. Each session has an explicit brand and stable ID; its recording date can be unknown and filled in later. In **Add file**, users can drop or select several local files. The panel uploads each selected file through Storage into `<brand root>/sessions/<date or undated>/<session ID>/`, then Catalog verifies the returned Storage file ID and exact folder before linking it. The folder stays fixed when a recording date is edited. Progress, cancellation, and retry are shown per file. If upload succeeds but linking fails, the file remains in Storage and its ID is shown for a link retry. Existing Storage files can still be attached by ID or chosen from the explicit read-only import preview.

Catalog never automatically scans Storage folders. Upload happens only after a user selects files and clicks **Upload selected**. Session cards and asset details show available image or Media previews, with original file playback for supported formats. `media.completed` updates cached state only for already linked assets.

The session page can filter its linked files by name, Storage ID or Media description, content type, Media duration, platform, and recorded post status. Lengths are read from Media only for the session's linked video and audio files when that page opens; missing lengths remain unknown. The filters do not scan Storage or change publication records.

Session and search file cards show cloud hosting badges such as **Bunny · Ready**, **Bunny · Processing**, or **Bunny · Failed**. They read the recorded hosting state in Catalog; displaying a badge does not upload a file or contact the host. Hovering a badge shows its connection and last check time when available.

The session page keeps a compact file-search field and **Filters** button above the asset grid. Content type, sharing status, platform, sort order, quick publication choices, and the duration range live in the filter modal. Active filters are summarized beside the button, with a count and **Clear filters** action. **Show files** returns to the grid; closing and reopening preserves the selected filters. **Reset filters** preserves the separate file-search text.

The session length filter has two slider handles and exact minimum/maximum inputs in seconds. Both endpoints are included. Range filtering only includes videos and audio with known Media durations; **Unknown only** shows videos and audio whose length is unavailable. **Any length** resets the range.

Clicking a file card opens a wide modal with its preview or player, metadata, review controls, cloud hosting, and platform posts. Close it with **Close**, Escape, or a click outside the dialog to return to the file grid. Review and hosting updates keep the file dialog open; post editing returns to the file dialog when finished.

## Asset display and source links

Session pages offer **Grid** and **Grouped by original**. The choice is remembered in this browser. Grouped view uses only recorded source relationships, with several source cards across a row and expandable, compact derivative cards below them. Nested derivatives keep their immediate parent, previews, descriptions, hosting badges and per-asset platform status. Shared derivatives appear once under their first linked in-session source; all source links remain available in the asset modal. Sources from another session can be opened from the same parent controls.

The filter modal offers **All assets**, **Sources / no parent linked**, and **Derivatives only**. Grouped filtering retains nonmatching parents as clearly marked **Context** and expands paths to matching derivatives. The displayed match count excludes context cards; Grid shows only matches. Source filters mean recorded parent presence, not proof that a file is an original. Badges use explicit `kind`/`relation` values for Original, Clip and Reel; missing parents are labeled **No source linked** rather than inferred from file names.

### Generic asset labels (0.6.0)

Every asset can carry a Catalog-owned **Favorite** flag, up to 25 normalized generic tags (for example `share-next`, `teaser`, or `best-take`), and one Patreon intent: **Unset**, **Free**, or **Paid**. The intent is a planning label and does not publish or change Patreon. Cards show the star and compact badges; the asset modal edits one asset, while the selection toolbar applies the same changes transactionally to up to 100 assets. Session filters and global Catalog search support favorites, exact tags, and Patreon intent alongside publication state. `content_catalog_assets_labels_update` accepts expected revisions so stale bulk edits fail without a partial update.

Asset detail, session lists and search return `sources` directly on each asset, including parent name, session ID, kind and recorded relation/order. One project-scoped SQL query enriches the whole page, including cross-session source metadata. Display changes do not write relationships, generate derivatives, scan folders or modify publication records.

## Current Media metadata

Asset detail, session asset lists and search return `description`, `description_source`, `description_updated_at`, `media_status`, `media_rating` and `duration_ms` directly on each asset. Media is authoritative; descriptions are read live and never persisted in Catalog. `probe_status: ok` maps to Catalog's existing `completed` status. Missing Media records return `missing`; read failures return `unavailable` with `media_error`, clearing stale response metadata.

Media 0.14.7 adds `media_get_batch`. Catalog reads only explicitly linked file IDs, deduplicated in batches of up to 100, without Storage enumeration, processing or URL signing. Global text search checks Media descriptions before pagination while retaining brand/session, publication and date filters. If Media fails during description search, the request reports that failure instead of silently returning incomplete matches. Lists still return linked assets when Media is unavailable. Cards display a compact description and Media status/rating; details show the full description and its provenance.

## Platform posts

A post records one destination outcome and may include several assets of the same brand, including assets from different sessions through the MCP tool. The session page shows each shared post once and shows its platform status icon on every member asset. Users can create or edit posts from the session or an asset. A post stores destination, account or tier, title, planned and actual times, external ID or URL, status, and evidence source. Status changes append an event. Verified live requires an external URL or ID, an actual time, and evidence. Once a post has verified publication history, its asset membership cannot change.

The old per-asset publication MCP tools remain as a compatibility interface. Migration 004 copies legacy per-asset rows into posts, grouping former release targets only when their shared fields agree. It preserves the old tables as an archive and never modifies other apps. Search and availability now read the post records. Hosting a video never implies external publication.

## Automatic session hosting collections

Only an explicit `content_catalog_hosting_request` (or **Host approved asset**)
can create a remote collection. The asset and its session must be active, the
asset approved, and the brand's video-host connection bound. Browsing, search,
imports, and existing-video backfill do not create collections or upload files.

The destination order is the session's `host_collection_id`, then the brand's
default collection. If both are empty, Catalog's optional provider collection
contract finds or creates a collection named exactly after the session title
(Bunny supports 1–100 characters). A single matching name in the selected
connection/library is reused; multiple matches require an explicit collection
ID. Lookup reads all pages before creating anything. Bunny is the first provider
with this capability; providers without it retain their existing upload flow.

Catalog saves the collection ID on the session and increments its revision.
Later uploads reuse that ID even if the session title changes. Existing hosting
records are checked first, so historical or backfilled videos are not moved or
transferred again to create a session collection. An explicit brand default
continues to be used without creating a session collection.

A durable reservation prevents concurrent requests or restarts from sending
another collection-creation call. If creation times out, a retry reads the host
to find the uniquely matching collection. If no unambiguous result is visible,
hosting stops; an operator can check the host and explicitly set the session's
collection ID. Revision checks preserve edits made during the request. A remote
collection already created remains on the host if saving the session fails;
its saved result is reused on retry. No automatic scan, publication, Storage
move/deletion, or Media mutation is involved.

## Boundaries

Cloud hosting remains an explicit request for an approved video. Bunny Stream is the first provider. A session may override its brand video-host collection through the session create/update MCP tools. The `content_catalog_hosting_link_existing` MCP tool backfills an existing Bunny video by GUID, verifies it through `get_video`, and records its library, collection, duration, ready status, and source evidence. It never calls `fetch_video`. A linked existing video blocks a later hosting request for the same asset. Media's source checksum can corroborate the Storage asset record; Bunny supplies no cryptographic proof that its video matches those bytes. The backfill action has no UI control. Catalog does not automatically host based on size, discover Media derivatives, sync Social or Patreon results, or publish externally. A human or external workflow records post evidence through the UI or MCP tool.

Run checks from this directory with `GOWORK=off go test ./...` and `GOWORK=off go build ./...`. Build the panel from the `apps` repo with `bun run scripts/build-panels.ts --app content-catalog`.


## Reversible archive (0.5.0)

Assets and sessions have an independent `lifecycle: active | archived`. Editorial approval remains unchanged. The **Lifecycle** selector offers **Active** (default), **Archive**, and **All** on sessions, session assets and search. Archive includes assets whose parent session is archived, even if their individual lifecycle is active. Asset details remain directly readable and show eligibility, archive reason/time, original session ID, revision and lifecycle history.

From an asset modal, use **Archive**, **Move** or **Restore**. Grid view also supports selecting several assets for a single operation. Archive requires a reason and can optionally move assets into a same-brand session. Create the destination session and use **Archive session** if it should be an archive collection. Session archive hides its assets from normal selection without rewriting their individual lifecycle. Restore the parent session first, or restore selected assets into an explicitly chosen active destination.

Moving changes Catalog membership only. It preserves asset IDs, Storage IDs, descriptions read from Media, source links, render references, hosting, post membership and existing audit history. Storage folders/files are never moved, copied or deleted. Restore defaults to each asset's original session; destinations must have the same brand and be active. A destination containing another link to the same Storage file causes the entire batch to fail rather than merging IDs.

The MCP tools `content_catalog_assets_archive`, `content_catalog_assets_move`, `content_catalog_assets_restore`, `content_catalog_sessions_archive` and `content_catalog_sessions_restore` are also available through HTTP `/action`. Every operation needs a stable `operation_id` and current expected revisions. Asset batches require `expected_revisions` mapping each asset ID to its revision. Supply `destination_revision` when changing sessions, or `destination_revisions` mapping session IDs for a batch restored to different original sessions. Exact retries return the saved result without another revision or audit event; reusing a key for different arguments fails. Stale revisions, missing destinations and cross-brand moves roll back the whole transaction.

### Agent selection policy

Use `content_catalog_search` with `availability=ready_to_publish`, the explicit brand/destination, and the default active lifecycle. Never use `lifecycle=archived` or `all` for ordinary processing, calendar or publishing selection. Before starting work from a cached selection, call `content_catalog_assets_eligibility` with the explicit asset IDs. Every item must have `eligible=true`. File-only checks also require the exact `storage_install_id`; mixed active/archive links require an explicit active asset identity. Missing records or failed eligibility reads must stop the workflow.

Catalog rejects hosting uploads, attaching/upload routing into archived sessions, and new planned/scheduled/submitted post records containing archived assets or sessions. Normal post listing excludes archive members; explicit archive/all inspection and direct historical reads preserve their evidence. Hosting readiness checks and existing-video backfill remain available for historical inspection; these do not transfer bytes or publish posts. Lifecycle changes emit `content-catalog.lifecycle.changed` for external consumers to invalidate cached selections.

This release changes **Content Catalog only**. It does not modify Media, Media Processing agents or external publication tools. Agents that call those apps directly must follow the Catalog eligibility policy; Catalog cannot intercept direct calls or cancel work already running in another app. Existing Holly records are not automatically archived or relocated on upgrade.

## Hosting progress and checksum waiting

An explicit **Host approved asset** action persists an intent tied to the exact
asset, Storage install/file, session, and video-host destination. If the checksum
is pending, Catalog shows **Waiting for checksum verification** and its Storage
state. The Storage checksum-ready event resumes that intent; a bounded worker
checks saved intents every 15 seconds as a fallback after restarts or missed
notifications. It checks only those explicitly requested files, never lists
Storage folders or creates hosting requests for other assets.

Before resuming, Catalog checks active asset/session lifecycle, current approval,
Storage binding, and the saved connection/library/collection policy. A manually
changed destination or revoked eligibility blocks the request for review.
Another asset's automatically created session collection can be reused. The
final approval and route are checked again before the provider transfer.
**Cancel waiting upload** stops an intent before transfer; it cannot cancel an
upload that already started. Failed checksums stop the request. Temporary read
errors back off, while ambiguous collection or video mutations need explicit
reconciliation and are never automatically repeated.

New uploads use an explicit MCP `title` when supplied, otherwise the current
Media title when available, otherwise `<session title> — <filename>`. The chosen
title is saved with the intent. Existing hosted titles are never renamed.

Catalog keeps `encode_progress`, numeric `provider_status`, `provider_stage`,
and the complete `transcoding_messages` from Bunny. Unknown progress is null;
unknown stage codes remain visible rather than being invented. Bunny's encoding
and upload failures are terminal. Only recorded remote videos in `processing`
are checked automatically; ready, failed, and unresolved transfers stop polling.
Provider read errors preserve the last observed state and retry with backoff.

Asset cards show observed stage/percentage or a checksum-waiting badge. The file
modal shows percentage, provider stage, messages, last check, and any refresh
error. Its progress refresh uses read-only Catalog records every five seconds;
session cards refresh every fifteen seconds without reloading Media or the
player. `content_catalog_hosting_list` accepts exactly one `asset_id` or
`session_id` and returns both `hostings` and `hosting_intents` without a provider
call. Manual **Check readiness** remains available.

These changes affect Catalog only. They do not modify Media, move/delete files,
scan folders, or publish to Social/Patreon. Upgrading creates no new hosting
intents for historical files. Existing pending remote uploads can be observed
automatically after upgrade; no historical checksum wait is silently converted
into permission to upload.
