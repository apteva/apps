# Storage 0.11.1

Storage provides project-scoped file metadata, virtual folders, uploads, search,
and sharing. Bytes live on disk or in a bound S3-compatible bucket. The Go
sidecar uses app-sdk v0.73.0; the build requires Go 1.26.8 or newer. The React
panel, file card, and native mobile surface share the HTTP API.

Version 0.11.1 returns HTTP 403 Forbidden when URL imports target blocked internal
addresses, including after redirects. The import policy and error text are
unchanged; genuine upstream failures retain their existing status handling.

## Upgrade from 0.10.26

Migration 006 adds persistent share keys and generations, completed-upload
receipts, backend identity, pending-upload reservations, and durable blob
cleanup. Back up the install database and object store before upgrading.

Behavior changes:

- Public routes accept public files or a valid share signature. A user identity
  alone does not authorize private content on those routes.
- `files_get_url` defaults to revocable Storage delivery on both backends.
  Canonical S3 URLs stream through Storage. Explicit `delivery=direct` remains
  available, and cannot be revoked before expiry. TTLs are capped at seven days.
- Existing token-derived share signatures expire on upgrade. New shares use a
  persistent install key; restarting or rotating an app token preserves them.
  Setting private or signed visibility revokes older Storage signatures.
- Public disk responses require revalidation. The current CDN app is a routing
  proxy with no object cache or purge API. Already downloaded/browser-cached
  copies and previously issued direct S3 URLs cannot be recalled.
- Direct PUTs target temporary objects. Finalization streams and verifies the
  actual size and SHA-256, then publishes a new immutable key. This adds one
  backend read and write. The client cannot overwrite the published key by
  reusing its original PUT URL.
- Upload deduplication requires the same project, digest, folder, and filename.
  Conflicting visibility, MIME type, or tags produce an explicit conflict.
  Update metadata separately; identical bytes at another destination create a
  distinct row and object.
- Traversal segments and invalid visibility are rejected. Folders use literal,
  case-sensitive matching. Names are limited to 200 UTF-8 bytes.
- Inline base64 uploads are capped at 25 MiB. Multipart and URL imports stream
  under `max_upload_size_mb` (default 100 MiB, maximum 10 GiB).

## Uploads, cleanup, and quotas

`POST /uploads`, `PUT /uploads/{id}/parts/{n}`, `GET /uploads/{id}`, and
`POST /uploads/{id}/complete` implement resumable uploads. MCP equivalents use
1 MiB chunks. HTTP and MCP parts share one atomic declared-byte allowance;
different parts transfer concurrently, while completion freezes the session.
Completion receipts remain for seven days, so retrying a completed request
returns the original file. Replacing committed parts is refused.

The browser hashes large files incrementally and saves only session metadata
in localStorage. Selecting the same file in the same project/install resumes
verified parts after a transient failure. Cancel deletes the session; closing
an interrupted browser leaves it available until the idle TTL expires.

The install defaults to 64 pending sessions and 1024 MiB of combined declared
size (`max_upload_sessions`, `max_pending_upload_mb`). Direct uploads retain
reservations until their PUT URL expires. Aborting a session releases its
reservation. Four full-file transfers run concurrently per process.

Hard deletion removes the catalog row and queues durable object cleanup.
`hard=true` means bytes were removed; `hard=false` can mean deletion is queued.
Soft deletion retains both metadata and bytes until an explicit hard purge.
Tombstone purges still enforce the file's folder permissions. Failed inserts
compensate object writes, and the sweeper retries remaining cleanup. It stops
when the sidecar unmounts.

## Backend selection and migration

Mount requires successful platform identity discovery. Missing or unavailable
binding data cannot silently select disk. The first mount pins the backend
identity; later endpoint/bucket/backend changes refuse to mount. Credentials
refresh every five minutes while preserving the original object location.

To migrate stores without changing file IDs or metadata:

1. Stop the install and retain its database backup. Finish/abort all upload
   sessions and drain the old backend's cleanup queue before migration.
2. Copy every retained object, including soft-deleted files, to the destination,
   preserving keys. Keep the old store intact until verification succeeds.
3. Configure the destination binding/bucket, then start the sidecar once with
   `STORAGE_VERIFY_BACKEND_MIGRATION=1`. This verifies the size and SHA-256 of
   every database-referenced destination object before updating the backend pin.
   Missing/mismatched bytes leave the old identity unchanged and mount fails.
4. Remove the flag for subsequent starts. Keep the old backup until the new
   store has been checked operationally.

Run only one sidecar against an install's database during migration.

## Query and URL contracts

HTTP `/files` combines `folder`, `recursive`, `q`, `content_type`, `sha256`,
`tag`, `source`, `limit`, and `offset`. MCP `files_list` and `files_search`
also accept offsets and return `has_more` and `next_offset`. Ordering includes
an ID tie-breaker. Permission filtering happens before pagination. Offset
pagination is stable for an unchanged catalog; restart a traversal after
concurrent mutations when a complete inventory is required.

URLs include project and install scope. Preserve the whole returned URL.
Public imports allow HTTP(S), validate DNS at connection time and on redirects,
and block private/link-local/loopback destinations by default. Administrators
may allow specific internal hostnames with `import_internal_hosts`.

## Build and validation

```sh
bun install --frozen-lockfile
bun run typecheck
bun run build
bun run test                          # Chrome, isolated browser fixtures
GOWORK=off go test -short ./...        # Tier 1
GOWORK=off go test -race ./...
GOWORK=off go test -tags integration ./... # Tier 2, real sidecar
apteva test --tier all ./scenarios/   # All tiers, including real LLM scenarios
```

Live S3 profile (disposable local MinIO; no production bucket):

```sh
docker run --rm -d --name storage-test-minio -p 127.0.0.1:19000:9000 \
  -e MINIO_ROOT_USER=storage-test -e MINIO_ROOT_PASSWORD=storage-test-password \
  minio/minio:RELEASE.2025-09-07T16-13-09Z server /data
GOWORK=off STORAGE_TEST_S3_ENDPOINT=127.0.0.1:19000 \
  go test -tags integration ./...
docker stop storage-test-minio
```

The profile verifies actual signed PUTs, checksum validation, immutable
publication, completion retries, ranged reads, revocation, and deletion.
Provider-specific connection/endpoint behavior also has unit fixtures. MinIO
coverage does not certify live AWS, R2, B2, or Hetzner accounts.
