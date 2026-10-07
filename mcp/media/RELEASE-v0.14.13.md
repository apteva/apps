# Media 0.14.13

Media adds an opt-in fast trim path and validates audio delivery against the
requested loudness. Frame-accurate trimming remains the default.

- `media_trim(trim_mode="auto")` selects the closest H.264/HEVC keyframes within
  `max_start_drift_ms` and `max_end_drift_ms` (250 ms each by default, 0–2000 ms
  supported). It copies compressed video in MP4/MOV, re-encodes aligned audio
  at its source sample rate, and bounds presentation using container edits.
  It compares every retained video timestamp against source packets and every
  opening/ending picture against source decoding. Unsuitable cuts fall back
  to accurate encoding, with a persisted reason. The final full decode and
  every-frame black/opening/ending checks run before upload.
- `resolved_params.trim_diagnostics` reports requested/actual boundaries,
  drift, algorithm/app version, effective codec/preset/CRF/pixel format,
  source evidence counts, fallback, and output colour. The existing
  `encoder_profile` applies to HEVC trims too and is now visible for trim in
  the UI. Alternatively choose `hevc_profile`: `legacy` (fast/CRF 18), `fast`
  (ultrafast/CRF 23), `balanced` (fast/CRF 20), or `quality` (slow/CRF 18).
  Resolution, source frame rate and bit depth remain unchanged. Hardware
  encoding is not included in this release.
- `timeout_seconds` accepts a per-job budget within the operator's
  `render_max_timeout_seconds` (default four hours). HEVC trims otherwise use
  a conservative estimate from `render_hevc_estimated_speed` (default 0.2×),
  plus validation and transfer reserves. The estimate, effective timeout and
  warnings appear at submission, in saved diagnostics, and in the UI. An
  impossible accurate job/fallback is rejected before encoding. Presets do
  not imply a guaranteed host speed; configure the estimate from observed
  performance. For a 575-second source at 0.2×, encoding alone is 2875 seconds.
- Normalize/speech-clean measure loudness first, apply measured two-pass
  loudnorm, and measure the encoded file. Delivery must be within 0.5 LU of
  `target_lufs` and no more than 0.1 dB above `target_peak_dbtp` (default −1.5).
  Unmeasurable or undeliverable targets fail without upload. Sample rate,
  stream starts/durations and complete decoding are verified. Copied video
  keeps existing presentation cutoffs, including hidden HEVC reference frames.
- HDR trim outputs explicitly identify their HLG/PQ base colour. Dolby Vision
  dynamic metadata and container signaling are not guaranteed in this
  workflow; `require_dolby_vision=true` rejects the request before rendering.
  This release does not claim Dolby Vision preservation.
- Frame-log collection now handles audio/video log interleaving, which could
  otherwise undercount pictures in validation. New algorithm revisions
  invalidate old trim/normalization outputs; successful request-cache reuse
  retains original validation and provenance.

Runtime: accurate trim and non-normalizing audio edits retain their existing
FFmpeg path. Auto copy uses Python 3 plus FFmpeg/FFprobe on the execution host
and falls back to accurate encoding if Python is unavailable. Normalization
requires Python 3 and fails clearly before source download if it is missing.
The helper uses only the Python standard library. The configured production
render host has Python 3. Default accurate trimming needs no Python.

Deepgram billing/credits and transcription are unchanged. This release does
not approve a final master or replace existing trial files.

Validation: full-source 86052 replay on the configured render host selected
14.005–589.160 s (+5/+160 ms), decoded all 17,248 expected pictures, matched
source opening/ending windows, retained 10-bit HLG/BT.2020, and reported both
stream starts at zero. Copy plus source checks took 7m45s on that busy host;
this is not an upload/render completion guarantee. Diagnostic 90831 normalized
to −20.04 LUFS against −20, at its 48 kHz source sample rate. This short test
does not claim the full source had the same original loudness defect.

Regression coverage includes H.264 and HEVC open GOPs, drift/fallback/budget
limits, encoded loudness/peak rejection, source precision, normalization after
a copied trim, local/remote helper parity, and real Media–Storage pipelines
with validation retained through request-cache reuse.
