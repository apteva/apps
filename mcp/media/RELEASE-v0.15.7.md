# Media v0.15.7 — per-request transcription settings

Transcription callers can now select `model` and `language` on
`media_transcribe` without changing installation configuration. This removes the
need for batch scripts to change and restore global defaults, which restarted
the shared Media process and forced connected agents to refresh.

Model, requested language and the diarization default are persisted with each
queued attempt. Workers use that saved snapshot. Automatic retries, resumed
attempts and ordinary retries without overrides keep the original settings even
if an operator changes the installation defaults later. New sources and explicit
new attempts resolve their omitted settings from the current defaults. Queue rows
created before this version are frozen once when first claimed after upgrade.

```json
{"file_id":"27706","model":"nova-2","language":"en","require_word_timings":true,"force":true}
```

`language` accepts a language tag, `auto` or `multi`. The existing install fields
remain operator-selected defaults. Requests never write those fields or request
an app restart. Concurrent requests cannot overwrite a running or pending
attempt's settings; conflicting requests return `transcription_request_conflict`
(HTTP 409 on REST). The queue response and transcript `options` identify the saved
model, requested language and diarization setting; failed upgrades expose these
on `last_attempt.options`. Transcript `language` continues to mean the detected
result language, rather than the requested language.

REST callers can use POST
`/media/:file_id/transcribe?model=nova-2&language=en&force=true`. Existing requests
without options remain compatible. Word preservation, validated audio identity,
source/timestamp checks and preservation of completed transcripts are unchanged.
The executor and Deepgram integration binding do not change.

Validation covers jobs with different models/languages, defaults changed after
queueing, provider failure and retry, conflicting in-flight requests, replacement
sources, option persistence through SQLite and REST status/response handling.
The transcription helper tests mock platform calls and verify that recovery uses
per-request options and refuses the legacy global-configuration fallback.

This release is limited to Media and transcription scripts. It does not include
Server or Core catalog, cache, logging or restart-audit changes. Publishing the
release does not deploy it or start transcription work in production.
