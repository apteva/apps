# Video sources

Social stages videos in its persistent install data directory when a verified
immutable source is unavailable. Staging checks filesystem type and available
space, verifies the whole SHA-256 and container, and removes owned files on
completion or failure. A file lock protects live uploads from orphan cleanup,
which removes unlocked owned spool files after 24 hours.

Storage 0.13.0 supports Range downloads but does not supply the version-bound
descriptor below. It therefore uses disk staging. Its checksum, revision, or
ETag alone does not enable the streaming shortcut.

## Streaming contract

Storage's existing `file.source` string is provenance (for example `human` or
`media-render`). Social leaves provenance unchanged and uses full disk staging
for strings, absent fields and null values. A `source` object is accepted only
as the explicit `storage-source/v1` contract below, at the top level or inside
`file`. Malformed, incomplete or conflicting advertised objects fail explicitly
instead of silently downgrading. URL minting, renewal and metadata refresh use
the same compatibility decoder.

```json
{
  "schema": "storage-source/v1",
  "storage_install_id": 16,
  "project_id": "project-id",
  "file_id": 95905,
  "content_revision": "immutable-content-version",
  "size_bytes": 1992299908,
  "content_type": "video/quicktime",
  "checksum": {
    "algorithm": "sha256",
    "value": "64-lowercase-hex-characters",
    "status": "verified"
  }
}
```

The descriptor promises that the whole-file checksum was verified against that
immutable object version. Storage must enforce that binding during delivery.

Social passes `expected_content_revision` to `files_get_url`. The mint response
must include the identical `source`, `file_id`, and a signed `url`. Renewals and
proxy fallback must preserve this descriptor. Bound delivery must not redirect.
The signed URL must enforce the content revision for provider fetches that do
not send Social's expectation header. Provider-fetch video URLs are minted
with the expected revision, and Instagram validates the bound container
before creating a Reel.

Every Range GET includes `X-Storage-Expected-Content-Revision`. Storage must
enforce the version, return 206 with the exact Content-Range and body length, and
include `X-Storage-Content-Revision` and `X-Storage-File-ID`. Identity and framing
are checked before Social exposes a buffered chunk to an uploader. A 412
response means `source_changed`. ETags are never interpreted as SHA-256.

`video_source_chunk_mb` controls the single source buffer, from 1 to 16 MiB,
defaulting to 4 MiB. Two video preparations may run concurrently. Provider
request bodies consume this reader without allocating the whole video.

## Recovery

Ordinary YouTube and membership uploads retain their original resumable session
and probe provider-confirmed progress before sending bytes. New session
fingerprints bind the verified source digest, size, Storage binding, file identity, connection,
and publication metadata. Older byte-stream fingerprints are verified once and
promoted on the same target and session.

Recovery authorization includes the immutable source descriptor when present.
Revalidation fetches metadata and container bytes rather than downloading the
whole verified source. Sources without the descriptor retain full staging and
checksum verification. Existing account, audience, revision and duplicate
guards continue to gate execution.

Legacy failures caused specifically by trying to decode a Storage provenance
string as a descriptor can use `post_delivery_revalidate` and
`post_delivery_recover` on their original target. Eligibility requires the exact
historical error for the unchanged source ID, no native operation/session or
publication identity, no submitted/accepted request or upload receipt, and
nonpublication diagnostics. Source metadata is resolved afresh and all bytes,
checksum and container are verified before granting recovery. The target,
revision, source proof and nonpublication evidence are checked again before the
one-target retry. The historical failure stays in the audit and attempt ledger.
Other validation failures are not made retryable by this compatibility path.
