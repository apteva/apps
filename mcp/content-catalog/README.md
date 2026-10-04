# Content Catalog

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

Asset detail, session lists and search return `sources` directly on each asset, including parent name, session ID, kind and recorded relation/order. One project-scoped SQL query enriches the whole page, including cross-session source metadata. Display changes do not write relationships, generate derivatives, scan folders or modify publication records.

## Current Media metadata

Asset detail, session asset lists and search return `description`, `description_source`, `description_updated_at`, `media_status`, `media_rating` and `duration_ms` directly on each asset. Media is authoritative; descriptions are read live and never persisted in Catalog. `probe_status: ok` maps to Catalog's existing `completed` status. Missing Media records return `missing`; read failures return `unavailable` with `media_error`, clearing stale response metadata.

Media 0.14.7 adds `media_get_batch`. Catalog reads only explicitly linked file IDs, deduplicated in batches of up to 100, without Storage enumeration, processing or URL signing. Global text search checks Media descriptions before pagination while retaining brand/session, publication and date filters. If Media fails during description search, the request reports that failure instead of silently returning incomplete matches. Lists still return linked assets when Media is unavailable. Cards display a compact description and Media status/rating; details show the full description and its provenance.

## Platform posts

A post records one destination outcome and may include several assets of the same brand, including assets from different sessions through the MCP tool. The session page shows each shared post once and shows its platform status icon on every member asset. Users can create or edit posts from the session or an asset. A post stores destination, account or tier, title, planned and actual times, external ID or URL, status, and evidence source. Status changes append an event. Verified live requires an external URL or ID, an actual time, and evidence. Once a post has verified publication history, its asset membership cannot change.

The old per-asset publication MCP tools remain as a compatibility interface. Migration 004 copies legacy per-asset rows into posts, grouping former release targets only when their shared fields agree. It preserves the old tables as an archive and never modifies other apps. Search and availability now read the post records. Hosting a video never implies external publication.

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
