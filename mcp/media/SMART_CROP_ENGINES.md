# Smart Crop engines

`smart_crop_engine` selects `hybrid` (default), `mediapipe_full` or `legacy` in app
configuration and on `media_crop`, `media_extract_frame`, `media_extract_reel`
and `media_preview_crop`. An omitted request value follows app configuration.
`crop_mode=center`, explicit pixel crops and `fit_mode=contain` bypass detection.
Choose `legacy` to use the retained 0.14.22 saliency/foreground/face planner.

`smart_crop_framing` selects `upper_body` (current default) or `widest_valid`,
per request or app configuration. `widest_valid` chooses the largest native
rectangle at the requested aspect ratio, positioned to retain supported pose.
For a 1920x1080 source at 9:16 this is 606x1080 after chroma alignment, instead
of the tighter 378x672 example. It does not request padding or output scaling.
A legacy fallback expands its original window without dropping its pixels;
its unresolved coverage warnings remain guarded. Explicit pixel crops bypass
this preference. Native dimensions are retained by image `media_crop` when
`output_width` is omitted; extract-frame/reel output sizing follows the existing
operation defaults.

MediaPipe Pose Full runs CPU inference on native, autorotated source frames,
locally or on the configured render host. Stills and direct extraction share
pose geometry. Supported face/scalp, shoulders, arms and hands determine the
portrait extent; legs are not mandatory. Head/hair and hand margins are estimates.
Reels sample approximately every 500 ms, capped at 256 samples, use VIDEO mode
tracking, fixed size/vertical origin and median-smoothed horizontal positions
projected into feasible pose bounds. Longer reels have a wider sample interval.
Sampled retention does not guarantee every-frame composition or human approval.
Hybrid checks the unique same-frame subject with YOLO11n Pose, including native
head/torso/arm coordinates, before accepting tracked coverage or geometric
impossibility. Contradictions, unsupported hands, missing head grounding and
width failures trigger bounded fresh Full IMAGE and RTMPose checks. Independent
head/torso agreement allows atomic whole-pose replacement; a corrupted primary
torso cannot veto it, and its old bounds are discarded rather than unioned.
Supported ears also ground back-facing subjects. Comparisons use the independent
subject scale without assuming an upright body, open eyes or a visible front face.
When person detection fails, three bounded rotated views provide additional
same-frame evidence; all coordinates return to native space and the output
remains unrotated. Pose/identity disagreement remains guarded.
Fresh Full retains detailed fingers; independent RTMPose/YOLO replacement uses
conservative wrist/hand and head-top estimates. Conflicting or missing support
stays unknown. No model is selected because its width fits. Head/torso disagreement
can reset the tracker up to eight times. All grounding/recovery shares a 30-second
CPU budget inside the 120-second extraction/inference deadline; exhaustion cannot
certify geometry or be cached as a successful pose decision.

Pure Full retains its earlier guarded same-frame weak-hand refresh. Visibility
and presence thresholds remain 0.5; confidence is never interpolated or carried
across occlusion. Model localization scores have separate meanings.

No padding is added implicitly. Incomplete wrists yield unknown coverage;
actions wider than the crop yield a coverage warning. Existing
`require_action_preservation=true` rejects unknown/oversized coverage before
queueing. `crop_fallback=contain` is the explicit full-frame fallback.
Unavailable runtime, failed source extraction or insufficient head/shoulder
geometry use the retained engine and persist the fallback reason. Such fallback
decisions are not saved in the pose decision cache.

Saved `pose_failure_summary` separates trusted geometric overflow, uncertain hands,
rejected primary skeletons, unresolved identity, source clipping and recovery limits
with timestamps and a mixed-failure classification. Samples retain original/fresh/
independent evidence, accepted bounds model, agreement tolerance, head/hand margin
contributions and the landmark span separately from the estimated required width.
A margin contribution does not prove those pixels are safe to remove.

Saved `crop_diagnostics` identify requested/effective engine, app/algorithm,
MediaPipe runtime, Full model SHA-256, source/evidence timestamps and effective
rectangle/path. `pose_attempt` retains Full identity, framing, analysed/valid/
invalid/uncertain counts, planning result and failed sample timestamps/categories
alongside `pose_samples`, even when `effective_engine=legacy`. Landmark evidence
records native coordinates, visibility and presence; weak tracked wrist evidence
and refresh outcomes are retained separately. A no-pose result has no invented
landmark confidence. Engines/framing have separate decision and render cache identities.

## Runtime

The embedded setup creates an isolated managed CPython 3.11 environment with
MediaPipe 0.10.21, NumPy 1.26.4 and OpenCV contrib headless 4.11.0.86. It bootstraps uv
0.9.9 from a platform-specific, SHA-256-verified wheel without requiring system
pip or venv. Model downloads are verified against the embedded model hash.
First use needs network access to official package/model distributors and space
for Python/dependencies; warm requests reuse the environment. Setup/inference
share the caller's deadline, process cancellation and host work admission.
Native frame extraction retries up to three times, with a 15-second per-attempt
limit inside the 120-second extraction/inference budget. Each attempt removes
the preceding temporary frame. Diagnostics retain extraction attempts per sample
and a safe failure code/timestamp on exhaustion, without source URLs or stderr.
End-of-source reel samples leave two nominal frame intervals before the reported
duration, because forward seeks inside the final display interval can be empty.
The actual requested seeks are saved; VFR evidence remains sampled.

Linux glibc amd64 and Darwin arm64 are verified. Other wheel/platform combinations
may be unsupported and use a visible legacy fallback. Windows and musl hosts
are not supported by this POSIX setup. Local operators can set
`smart_crop_python` to an existing isolated compatible Python interpreter;
version and model hashes are checked on every invocation. Remote hybrid runtimes live
in disk-backed `/var/tmp/apteva-media-pose/mediapipe-0.10.21-hybrid-1`; pure Full
retains `/tmp/apteva-media-pose/mediapipe-0.10.21-full-1`; local managed runtimes live
under the OS user cache directory. Media pixels are never sent to an inference
service.

## Regression comparison

Existing photographic and algorithm benchmarks explicitly select legacy where
they assert legacy geometry. Real-runtime tests require private external pixels:

```sh
MEDIAPIPE_FIXTURE_DIR=/path/to/comparison-fixtures \
MEDIAPIPE_TEST_PYTHON=/path/to/isolated/python \
GOWORK=off go test -run '^TestMediaPipeFullLocalIntegration$' -v
```

The fixture manifests describe native source images and source reels; the test
uses production preprocessing and FFmpeg plans and saves review artifacts next
to those private fixtures. Do not commit customer pixels or source URLs.
