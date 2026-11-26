# Media v0.17.1 — probe first, one lease per file

## Why

On prod, H Sites render outputs stayed at `probe_status=pending` for over three
hours, and their transcripts failed with "audio duration unavailable". The index
workers were not dead. In component mode each index stage (probe, thumbnail,
every keyframe) took its own lease in the shared 2-unit admission queue. Renders
take both units for 15–50 minutes, so every stage waited behind the render
backlog again. The probe was only saved after the last keyframe, so the duration
stayed missing the whole time. `media_reindex` could not help, because the live
worker still held the file and a second attempt was skipped silently.

## Changes

- Remote indexing takes one admission lease for the whole file, before the
  source URL is signed, so later stages never requeue behind renders.
- The executor probe is saved as soon as it returns, as the local path already
  did. `media.indexed` and the transcriber notification fire then. If
  thumbnails or keyframes fail afterwards, the probe stays and the derivations
  are retried by the repair queue in 10 minutes (`remote_derivations_failed`).
- Transcripts are not claimed while their source is pending, or failed with
  fewer than 3 consecutive index failures. After that they fail with
  `media_index_failed: probe <status>: <error>` instead of a missing duration.
- `media_reindex` on a file a worker is indexing returns `in_flight: true`
  instead of `queued: 1`. `media_index_status` marks such rows `in_flight`.

## Verification

- New tests:
  - The probe is saved and its transcript claimable while the thumbnail stage
    is still blocked, under a single lease. This test fails on v0.17.0.
  - A claim left by an interrupted worker is reclaimed after 15 minutes, and a
    second worker finishes the probe.
  - A transcript waits through indexing and retries, then fails with
    `media_index_failed`.
  - Reindex reports an in-flight file.
- Full suite: only `TestReelHEVCAndSilentSourceTimeline` fails, as before.
