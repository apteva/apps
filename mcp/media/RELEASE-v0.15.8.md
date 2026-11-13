# Media v0.15.8 — durable capacity waits and audio extent preservation

Capacity-constrained renders retain the same durable request and render ID until
their overall deadline. Waiting past the former 600-second admission limit no
longer fails work that has not started. FIFO admission and weighted host capacity
remain enforced across renders, indexing and transcription audio preparation.

Render tools and REST render params accept `overall_timeout_seconds` (30–604800;
default 86400). Media saves an absolute deadline at submission, covering queue,
cache-lock, execution and validation waits. Restart does not renew the deadline.
The existing `timeout_seconds` remains the separate post-admission execution
budget. The legacy `render_admission_timeout_seconds` setting becomes a diagnostic
threshold rather than a terminal capacity-wait limit.

`media_get_render` exposes queue age, deadline, execution-start evidence, capacity,
queue position, work weight and bounded blocking-lease identities. For compatibility
claimed jobs may retain `status=running` while `metrics.stage=admission` and
`metrics.execution_started=false`. `render.started` is emitted after admission.
Capacity deadline expiry, overall expiry during processing and execution timeout
have distinct error codes. Capacity expiry says execution did not start and an
explicit new request is required for a retry. Waiting work is deferred under the
same ID and deadline during shutdown; cancellation still terminates it. Existing
component identities and persisted receipts continue to reconcile remote work.

Audio redaction now pads/trims the decoded stream to its intended sample extent,
retaining the original audio presentation endpoint independently of the video.
The operation applies no loudness normalization or gain correction. Standalone
MP3 priming remains excluded from the decoded programme. MOV/MP4 output clocks
preserve both sample and source edit precision. The audio-redaction algorithm
revision changes to invalidate older result-cache entries.

Failure/success receipts include expected and observed audio start, duration,
endpoint, source/output decoded sample counts and drift. Complete decode,
non-finite samples, channel/sample-rate, copied video packets/timestamps, colour
and decoded-picture validation remain enforced. Duration tolerances are unchanged.

The original source and all 15 ranges of production job 8558 reproduced the old
failure: 402.702 seconds expected versus 402.645333 seconds encoded. The updated
runtime retains 402.702 seconds, 96 kHz audio and all 24,135 video frames. Regression
coverage includes AAC presentation padding, delayed audio, unequal audio/video
endpoints, MP3 priming, queue saturation, cancellation, deadline persistence and
shutdown recovery. Private production media is not included in the release.

No Server or Core changes are included. The existing executor release is retained;
Media supplies the updated guarded runtime with each processing request.
