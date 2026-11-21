# Media v0.17.0 — local Parakeet transcription

## Why

Transcription required the Deepgram integration, which bills per request. NVIDIA
Parakeet TDT 0.6B v3 (int8) runs on CPU at roughly 15–20× real time on four
Apple M1 Pro cores. On French test calls it matched Deepgram nova-3 to within
2–3% word error, and it returns word-level timings.

## Changes

- New engine: a transcript whose frozen `model` starts with `parakeet` runs
  Parakeet through sherpa-onnx 2.0.2 instead of calling Deepgram.
- `transcribe_engine` sets the default model for new jobs:
  - `deepgram` (default) keeps current behaviour.
  - `parakeet` always uses Parakeet.
  - `auto` uses Parakeet unless the job needs diarization or a language
    outside its 25 European languages.
  - A per-request `model` still wins.
- Parakeet needs no integration. With `engine=deepgram` and no integration,
  explicitly requested Parakeet rows still run; Deepgram rows keep waiting.
- Placement: on the media executor when `execution_mode=component`, otherwise
  on Media's host.
- First use provisions a pinned runtime under a versioned root: uv 0.9.9,
  Python 3.12, sherpa-onnx 2.0.2 and numpy 2.2.6. It then downloads the
  ~490 MB model archive and checks every extracted file against a pinned
  SHA-256. A size/mtime marker avoids rehashing on later jobs. Setup is
  locked across processes.
- Runtime behaviour:
  - Audio is cut every ~30 s at the quietest point.
  - Word ends ignore punctuation tokens, which would otherwise absorb the
    following pause.
  - Words emitted on one frame are spread to the next word and counted as
    `estimated_words`.
  - Every word has a positive interval inside the audio.
- Results go through the same audio-identity, duration and word-timing
  validation as Deepgram. Segments split at sentence punctuation or pauses
  over 1 s. Provider is `parakeet` and word source is `parakeet_tdt`.
- Executor plans for Parakeet weigh two work units, and so does local
  admission. `transcribe_parakeet_threads` defaults to 4.

## Limits

- No diarization.
- 25 European languages; the detected language is not reported.
- First use on a host downloads ~490 MB and needs about 1 GB of RAM per job.

## Verification

- Unit and worker tests cover parsing, segments, engine defaults, executor
  script and weight, pinned hashes, and the end-to-end worker with the real
  `setup.py` against a staged runtime root.
- Live local run (`MEDIA_PARAKEET_LIVE_AUDIO`):
  - First run, including provisioning and download: 100 s for a 20 s call.
  - Warm: 10 min of English in 27 s, 1,192 words, 7 estimated.
- Not yet run on a remote executor host.
- `TestReelHEVCAndSilentSourceTimeline` fails as on v0.15.8 (pre-existing).
