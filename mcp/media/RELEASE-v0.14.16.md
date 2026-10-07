# Media 0.14.16

Normalization now automatically retries encoded true-peak overshoots with
additional headroom, retaining the requested loudness target. Uploads report
Storage quota/rate-limit failures without falling back to an oversized
whole-file request. Validation shows an accurate, indeterminate stage.

- Audio normalization makes at most three encodes from the original source.
  Each attempt measures the encoded LUFS and true peak. On peak overshoot,
  the internal ceiling drops by at least 1 dB, with a -9 dBTP lower bound;
  the requested LUFS/peak limits and existing tolerances stay fixed. Saved
  diagnostics include all attempt measurements, effective filter and retry
  count. Loudness-only failures stop immediately. Outputs must pass final
  sample-rate, timeline, color, decode and video-picture/evidence checks.
  Video evidence is reused only after the existing identity, packet and
  presentation checks; retries do not cause extra video validation scans.
- Remote upload initialization falls back only for unsupported endpoints
  (404/405/501). Quota exhaustion stops with the original Storage response,
  phase, HTTP status and an actionable error. Transient 429/5xx/network
  failures have bounded backoff (four attempts, delay capped at 10 seconds),
  with Retry-After retained. Local resumable uploads apply the same quota/
  HTTP retry policy. Old-server whole-file fallback rejects outputs above
  a conservative limit below the platform's 1 GiB request-body cap. Error
  truncation retains the final failure cause as well as initial context.
- Overall encode/copy progress reserves space for later work. Validation
  remains at an approximate 80% overall and exposes no stage percentage,
  because one scan cannot estimate all remaining comparisons. Upload is
  indeterminate at 95%; only successful completion reaches 100%. Live queue
  events preserve stage metrics instead of replacing them with encode
  percentages. Accurate trims without the Python runtime also announce
  remote validation before decoding.

Includes the v0.14.14 last-frame/analysis-window fixes and v0.14.15 shared
validation, evidence reuse, live execution mode and verified cancellation.

Validation: 592 top-level cases passed in the full race suite, with 25 optional/environment cases skipped. The three private production trim/frame-retention replays passed separately, as did the final upload race regressions, six Media–Storage integration checks, Go vet, Darwin arm64/Linux amd64/Linux arm64 builds, and all four UI host-import checks.

A temporary audio-only replay of the reported production source reproduced -19.92 LUFS / -1.32 dBTP on the first encode. The automatic retry kept the original -20 LUFS / -1.5 dBTP request and passed at -20.01 LUFS / -2.89 dBTP, independently measured with FFmpeg. The temporary remote output was removed; no full video rerender, upload, master approval or original archival was performed.
