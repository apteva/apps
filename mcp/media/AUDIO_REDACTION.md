# Transcript-driven audio redaction

`media_redact_audio` submits a normal asynchronous Media render. It creates a
new Storage file and leaves the original intact. Poll `media_get_render` or
wait for the existing render completion/failure/cancellation notification.

```json
{
  "file_id": "99846",
  "selections": [{"text": "phrase to hide", "occurrence": 1}],
  "method": "bleep",
  "preview": true
}
```

A selection is either text plus an optional 1-based occurrence, or a half-open
`{start_ms, end_ms}` interval on the original source timeline. Text matching
ignores case and surrounding punctuation. Repeated matches require an explicit
occurrence. Up to 32 selections are supported; overlapping selections merge.
The queued receipt returns the resolved, padded ranges and source identity.
The same immutable plan is used when the worker executes; a changed source is
rejected rather than silently editing a different passage.

Text selections require source-matched word timing. New Deepgram transcripts
retain it in `segments[].words` as `{text,start_ms,end_ms}`. Imported transcripts
can supply the same shape through `media_set_transcript` or the transcript HTTP
endpoint. Existing transcripts without timed words must be retranscribed or
edited using explicit timestamps. Deepgram word timing is mapped from audio
zero to the video timeline when audio starts late; leading gaps are represented
as silence without moving speech. Imported word times use the source timeline.
Video sources with nonzero container origins must first have their timeline
normalized; unsupported or changed timeline metadata is rejected. Sentence timings are never divided into
invented word boundaries. ASR alignment can still be imperfect: listen to the
preview when selecting a sensitive word.

Methods:

- `room_tone` (default): requires `room_tone: {start_ms,end_ms}`, a caller-reviewed
  background-only donor from the same source, 100–5000 ms long, outside every
  edited passage/fade. Media loops the ambience with small fades at its seams.
  It does not guess speech-free content or synthesize replacement speech.
- `mute`: removes the selected audio.
- `bleep`: replaces it with a 1 kHz tone.
- `muffle`: replaces it with quiet low-pass audio. This is an effect; it does
  **not** guarantee that the words become unintelligible.

`padding_ms` defaults to 40 (0–250 supported); `fade_ms` defaults to 10 (0–50
supported). The original is completely suppressed inside each resolved range
for room tone, mute and bleep. Fades sit outside those ranges. All channels are
processed; other audio tracks and subtitles are not copied into the output.

`preview:true` produces an audio-only excerpt with 1.5 seconds of context on
both sides. A preview is capped at 30 seconds; request distant selections
separately. Its receipt maps the excerpt back to the source timeline. Preview
filenames default to M4A. A full render defaults to the source container; an
explicit audio output extension can also extract just the redacted audio.

Full video outputs copy the compressed video and verify every packet's data,
PTS, DTS and duration. Existing source-bound decode evidence is reused where
available; otherwise the output video is decoded once. MOV/MP4 presentation
boundaries are retained, including the last frame. The encoded audio is fully
decoded and checked for invalid samples, stream start, duration, channel count
and sample rate before upload. Validation failure prevents upload.

Python 3 and FFmpeg are required on the execution host. The existing admission,
thread budgets, cancellation, source cache and render timeout apply. This tool
does not change the existing normalization algorithm.

Regression tests: `GOWORK=off go test -run 'TestAudioRedaction|TestDeepgramWordTiming' .`
They execute real FFmpeg using both local runtime and remote-script paths,
cover the four effects, stereo preservation, unchanged surrounding PCM,
source/transcript identity, ambiguous text, invalid bounds, preview mapping,
encoded validation and retention of all 150 frames in a five-second fixture.
