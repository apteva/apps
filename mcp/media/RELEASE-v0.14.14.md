# Media 0.14.14

Audio normalization retains the last picture of short clips, and read-only
analysis distinguishes between-frame window starts from genuine video gaps.

- Normalize/speech-clean preserve the exact rational MOV/MP4 presentation edit
  durations and movie clock. They no longer reconstruct these boundaries by
  rounding down ffprobe media durations. This retains the last picture of
  production source 90825 while preserving exclusive copy-trim cutoffs and
  hidden HEVC reorder dependencies. Every retained decoded picture and its
  timestamp are compared with the source before upload. Saved diagnostics
  include expected/checked picture counts, picture-match status and boundary
  mode. A new normalization cache revision prevents reuse of older outputs.
- Partial video analysis decodes keyframe preroll and records whether the
  preceding picture covers the requested window start. A first picture after
  that boundary no longer produces a false VIDEO_START_GAP when coverage is
  confirmed. Preroll pictures are excluded from the window's frame count,
  black scan and sampled metrics. Real opening gaps remain warnings; strict
  render validation still requires video and audio to begin at zero.
- Accurate trimming, auto-keyframe drift limits, HDR colour handling, encoded
  loudness/peak validation and existing saved validation remain unchanged.

Validation includes the exact production five-second 90825 regression,
local/remote helper parity, all-picture identity/timestamp checks, retained
HEVC copy cutoffs, between-frame windows and genuine 209 ms opening gaps.
The new source/output picture comparison adds a source decode to normalization
validation; it performs no video re-encoding. Existing files are not replaced.

Release verification: 578 top-level race tests passed, including private
short-source regressions; 22 environment/optional cases skipped. Five real
Media–Storage integration cases passed. Go vet and Darwin arm64/Linux
amd64/arm64 builds passed. The production render host replay retained all
150 pictures of source 90825 with matching decoded identities/timestamps,
zero video start, 10-bit HLG, 48 kHz audio, −19.9 LUFS and −2.87 dBTP against
requested −20 LUFS / −1.5 dBTP. Analysis-window coverage was also verified
on that host. These checks created no uploaded replacement or final master.
