---
name: how-to-use-storage
description: Upload, organise, search, share, and delete files with Storage.
command: /storage
metadata:
  category: storage
  icon: 🗂️
---

# Storage conventions

Files are addressed by numeric ID within a project and install. Preserve both
routing parameters in returned URLs. Folders are virtual metadata paths such
as `/reports/2026/`, beginning and ending with `/`. Do not use `.` or `..`
segments. Names retain their extensions and may contain spaces; the maximum
is 200 UTF-8 bytes.

An omitted folder means root `/`. Suggested folders include `/inbox/`,
`/exports/`, `/imports/`, and `/archive/`. Follow the user's requested location.
`/.media/` contains generated internal media artifacts; use the original file
when sharing unless the user asks for a derived artifact.

## Upload and deduplication

`files_upload` accepts base64 for files up to 25 MiB. Set `content_type`,
`source`, and the intended visibility. For larger content use HTTP resumable
uploads or `storage_upload_init`, `storage_upload_part`, and
`storage_upload_complete`. MCP parts are capped at 1 MiB. Complete only after
all contiguous parts have uploaded. Status lists saved parts; completion is
retry-safe for seven days. Abort unwanted sessions to release their quota.

`files_from_url` imports an HTTP(S) resource with the requested name, folder,
tags, and visibility. Internal network imports require an administrator's
explicit hostname allowlist.

`was_existing=true` requires the same project, SHA-256, folder, and filename.
Identical bytes at another destination create a distinct file. A metadata
conflict is an error; update visibility or tags explicitly instead of assuming
an upload changed them. `files_dedupe_check` finds a readable matching digest,
which can be at a different destination.

## Listing and changes

Use `files_list` for one folder and `recursive=true` for descendants. Use
`files_search` to combine name, folder, MIME prefix, digest, literal tag, or
source filters. Continue with `next_offset` while `has_more=true`. Concurrent
changes can shift offset pages, so restart when a complete inventory matters.

`files_move` accepts a new folder and/or name. `files_rename_folder` changes a
whole subtree atomically and requires write access to every source and
destination descendant. A denied descendant prevents the whole operation.
All metadata mutations emit `file.updated` events.

## Sharing

- `private`: protected project access; an explicitly minted signed link may
  also grant access.
- `signed`: time-limited links and protected project access.
- `public`: anyone can read until visibility is restricted or the file deleted.

A request for a shareable link authorizes minting a time-limited link; it does
not require making the file permanently public. Use `files_get_url` with
`delivery=apteva` or `delivery=proxy` for revocable delivery. The default is
`apteva`. Setting visibility to private (even when already private) revokes
older Storage links. TTL is capped at seven days.

Only explicitly choose `delivery=direct` when the recipient needs a backend
URL and the user accepts expiry-based access: direct S3 URLs bypass later
Storage visibility changes until expiry. Already downloaded copies cannot be
recalled. Use the user-authorized audience and avoid widening it implicitly.

## Deletion

`files_delete` defaults to hard deletion. The catalog row disappears and
failed object removal is queued for retry; `hard=true` confirms immediate
byte removal. `keep_record=true` retains a tombstone and the bytes until an
explicit hard purge. Soft-delete followed by hard-delete is supported and
still checks the original folder's permissions. Follow the user's deletion
instruction; ask when their intended scope is unclear.
