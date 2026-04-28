# Storage (v0.1)

URL-addressable file storage for Apteva agents and human teams.

## Surfaces

- **12 MCP tools** — upload, get, get_url, search, list, list_folders,
  move, set_tags, set_visibility, dedupe_check, delete, from_url
- **REST surface** at `/api/apps/storage/*` for the dashboard panel
- **Files panel** (vanilla HTML+JS) — folder browser with breadcrumbs,
  upload via drag-drop or button, share-link mints a signed URL
- **Three URL flavours** for serving content:
  - `private` (default) → needs Authorization header
  - `signed` → HMAC-verified URL with expiry, no auth needed
  - `public` → open URL, full CDN-style cache
- **Range requests + ETag + Cache-Control** for free

## Folders

S3-style: a folder exists when there's a file in it. No separate
`folders` table, no parent-child relations. The agent can:

- `files_upload({folder: "/reports/2026/q1/"})` — implicitly creates the path
- `files_list({folder: "/reports/", recursive: true})` — descend
- `files_list_folders({parent: "/"})` — top-level layout overview
- `files_move({id, folder: "/archive/2025/"})` — atomic reorg

## Local development

```bash
cd mcp/storage
go build .
APTEVA_PROJECT_ID=test STORAGE_BLOBS_DIR=/tmp/blobs ./storage
curl http://localhost:8080/health
```

## Tests

```bash
go test ./...                       # tier 1, ~50ms
go test -tags integration ./...     # tier 2, ~1s — real binary, real HTTP
apteva test ./scenarios/             # tier 3, ~2min — real LLM
```

## Out of scope for v0.1

- S3 / R2 / MinIO backend — local disk only (storage_key is opaque,
  swap is config-only at v0.2)
- Image transforms / thumbnails — agents do their own at upload time
- Quota / GC — soft-delete just marks the row; bytes stay on disk
- Multi-region — single host serves everything
