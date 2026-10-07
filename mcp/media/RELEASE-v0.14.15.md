# Media 0.14.15

Copy trims now perform picture, black-frame and timeline validation in one
output decode. Live status identifies the selected execution mode, and
remote cancellation confirms worker shutdown instead of hiding stop errors.

- Auto keyframe trims combine decoded hashes, every-frame black checks and
  video/audio timeline checks in one FFmpeg pass. The local and remote
  executors consume that checked evidence rather than decoding the output
  again. Source opening/ending picture comparisons remain in place.
- Newly validated outputs carry video evidence bound to the uploaded file's
  SHA-256. Normalize/speech-clean reuse this evidence only after checking
  fresh Storage identity, copied packet hashes/timestamps, presentation
  boundaries and video properties. Indexing need not have completed.
  Missing, old or mismatched evidence keeps full picture validation. Audio
  measurement, finished loudness/peak checks and decoding remain mandatory.
  New cache revisions prevent reuse of outputs from earlier algorithms.
- Auto trims initially report `selecting`, then publish `keyframe_copy` or
  `accurate` as the executor commits to that mode, before validation ends.
  Local status is streamed; remote status uses atomic snapshots polled every
  five seconds. The chosen mode survives validation snapshots. API metrics
  expose `stage` and `stage_progress_pct`; the panel/card show copy/encoding,
  validation and upload phases. Upload is indeterminate until Storage
  confirms completion; an encode reaching 99% no longer labels upload 99%.
- Remote stops explicitly invoke Bash, terminate the dedicated process group
  with TERM, escalate to KILL if necessary, and verify no live workers remain.
  Exit codes and failure output are checked and persisted. Normal completion
  joins and stops the watcher, preventing a later deferred cancellation from
  killing a completed job. A startup guard prevents a late worker launch
  after cancellation; persisted host/workdir identity also supports orphan
  cleanup after a sidecar restart.
- Cancellation of an active job returns `cancellation_requested=true`,
  `cancellation_confirmed=false` and keeps `status=running` until its worker
  finishes cleanup. Poll `media_get_render` for confirmation. A failed
  remote stop yields `remote_cancellation_failed`, with saved diagnostics,
  rather than a falsely confirmed cancelled status. Pending and already
  terminal cancellation remain idempotent.

The v0.14.14 last-picture retention and between-frame analysis fixes remain.
Accurate cuts, zero-start checks, every-frame validation, HLG signalling,
encoded audio validation and source preservation remain supported. No
originals are archived or replaced by this release.

Validation includes command tracing proving one full copy-output decode and
zero normalization video decodes when trusted evidence matches, stale-proof
fallback, fresh Storage identity checks, real Media–Storage provenance/cache
reuse, live mode/phase reporting, default-shell process-group termination,
forced KILL, orphan failures and prevention of late kills. The configured
production render host passed a short HEVC trim/normalization evidence replay
and an isolated synthetic worker stop. No new full Marionette render or
master approval was performed; its reported 17,360-frame copy was not an
encoding-speed defect.

Release checks: 588 top-level cases passed in the full race suite, with 22
optional/environment cases skipped. The additional process-inspection
failure regression and targeted cancellation race tests passed. Six real
Media–Storage integration checks, Go vet, Darwin arm64/Linux amd64/arm64
builds, and all four UI host-import checks passed. Existing private short
trim/loudness/frame-retention regressions remain green. Production was
checked using temporary outputs and synthetic workers only.
