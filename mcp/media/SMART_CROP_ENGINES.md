# Smart Crop engines

`smart_crop_engine` selects `mediapipe_full` (default) or `legacy` in app
configuration and on `media_crop`, `media_extract_frame`, `media_extract_reel`
and `media_preview_crop`. An omitted request value follows app configuration.
`crop_mode=center`, explicit pixel crops and `fit_mode=contain` bypass detection.
Choose `legacy` to use the retained 0.14.22 saliency/foreground/face planner.

MediaPipe Pose Full runs CPU inference on native, autorotated source frames,
locally or on the configured render host. Stills and direct extraction share
pose geometry. Supported face/scalp, shoulders, arms and hands determine the
portrait extent; legs are not mandatory. Head/hair and hand margins are estimates.
Reels sample approximately every 500 ms, capped at 256 samples, use VIDEO mode
tracking, fixed size/vertical origin and median-smoothed horizontal positions
projected into feasible pose bounds. Longer reels have a wider sample interval.
Sampled retention does not guarantee every-frame composition or human approval.

No padding is added implicitly. Incomplete wrists yield unknown coverage;
actions wider than the crop yield a coverage warning. Existing
`require_action_preservation=true` rejects unknown/oversized coverage before
queueing. `crop_fallback=contain` is the explicit full-frame fallback.
Unavailable runtime, failed source extraction or insufficient head/shoulder
geometry use the retained engine and persist the fallback reason. Such fallback
decisions are not saved in the pose decision cache.

Saved `crop_diagnostics` identify requested/effective engine, app/algorithm,
MediaPipe runtime, Full model SHA-256, source/evidence timestamps and effective
rectangle/path. Engines have separate decision and render cache identities.

## Runtime

The embedded setup creates an isolated managed CPython 3.11 environment with
MediaPipe 0.10.21, NumPy 1.26.4 and OpenCV contrib headless 4.11.0.86. It bootstraps uv
0.9.9 from a platform-specific, SHA-256-verified wheel without requiring system
pip or venv. Model downloads are verified against the embedded model hash.
First use needs network access to official package/model distributors and space
for Python/dependencies; warm requests reuse the environment. Setup/inference
share the caller's deadline, process cancellation and host work admission.

Linux glibc amd64 and Darwin arm64 are verified. Other wheel/platform combinations
may be unsupported and use a visible legacy fallback. Windows and musl hosts
are not supported by this POSIX setup. Local operators can set
`smart_crop_python` to an existing isolated compatible Python interpreter;
version and model hashes are checked on every invocation. Remote runtimes live
in `/tmp/apteva-media-pose/mediapipe-0.10.21-full-1`; local managed runtimes live
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
