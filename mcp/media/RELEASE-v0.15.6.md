# Media v0.15.6 — preserved word timing and safe transcript upgrades

Deepgram word timestamps could disappear before Media persisted a transcript:
the integration removed channel-alternative word arrays, and Media did not read
utterance word arrays. Media now handles both formats, selecting one word stream
per channel without duplicating repeated words. Sentence text and boundaries
remain available alongside `segments[].words`, including optional confidence,
speaker and channel. All provider timestamps use rounded integer milliseconds.

The existing Deepgram `listen` catalog tool must be updated with this release.
It retains word arrays, provider request ID and audio SHA-256; no new integration
tool or binding is required. Refresh the integration catalog before upgrading
Media. Direct integration responses are consequently larger; Media offers the
bounded retrieval surface for consumers.

Transcription uses Media's fully decoded, validated audio preparation for both
audio and video. Saved timing evidence identifies the original source hash,
transcription-audio file/hash, preparation recipe, decoded duration and mapping
to the source presentation timeline. Nonfinite, missing, backwards or out-of-bounds
word intervals fail validation; sentence timings are never divided into word
intervals. Provider audio hashes are checked when returned. Speech recognition
coverage remains explicitly unverified.

`media_get_transcript` and REST retrieval expose `word_count`,
`timing_granularity`, `word_timing_status`, `source_matches`, `redaction_ready`,
`timebase` and a transcript revision. The existing full response remains the
default. Optional views provide summaries, text, paginated segments or words,
time windows, channel/speaker filters, exact normalized phrase matching across
sentence boundaries, and one-based occurrence selection. Cursors bind the file,
revision and filters; changing the transcript requires restarting pagination.
Word indexes identify occurrences without deduplicating repeated text.

`media_transcribe` accepts `require_word_timings=true` to upgrade missing or
unverified Deepgram timing. A completed transcript is snapshotted transactionally
before a retry is queued. Callers continue to receive the completed version with
`last_attempt` status while the candidate runs or after it fails. Promotion is
atomic and checks the source hash and claim identity. `force=true` no longer
deletes a completed transcript. Missing word timing blocks text-based redaction.
Multichannel text redaction requires explicit source timestamps; retrieval keeps
all channels separately accessible.

Saved Deepgram responses can be imported through `media_set_transcript` or the
REST import endpoint using `deepgram_response`, `source_sha256` and
`transcription_audio_file_id`. The response's audio hash must match Media's
source-matched, validated audio. Responses without sufficient identity evidence
must be regenerated through Media. Normalized manual imports remain supported,
with interval validation and source-checked commits.

Validation covers the original private 841-word response and an anonymized fixture
through parsing, SQLite, MCP and HTTP serialization; both Deepgram formats,
duplicate representations, repeated phrases, speaker/channel zero, cursor
invalidation, audio identity, source changes, failed upgrades, missing-word
responses, timeline offsets and a final-word mute with 50 ms padding. Existing
FFmpeg audio-redaction and signal-validation checks are included in the app's
short test suite. Media uses App SDK v0.115.1, verified as a descendant of v0.111.0.

Publication does not initiate deployment, backfill, processing or corrections.
Executor v0.15.1 and its immutable source pin remain unchanged.
