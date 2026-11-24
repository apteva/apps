# Conversations v0.32.0 validation — 2026-11-24

On prod (agent 11, main thread, Claude subscription / opus) a reply sat on
"Thinking…" for 93 s. Telemetry showed the cause: the request was refused
as over budget (102,390 > 101,581 estimated tokens, because Core used the
131k unknown-model default window for "opus"), and Core spent 79 s
summarizing history before retrying. Nothing in the chat said so.

Change:

- New `compaction.go`. Conversations turns Core's compaction events into one
  activity row named `context_compaction`:
  - emergency recovery opens it on `llm.provider_error` with class
    `context_length_exceeded`, records sizes from each
    `llm.context_recovery` pass, and completes it on the retried
    `llm.start` (fails on `llm.error`/`llm.err`);
  - planned compaction opens it on `llm.compaction_started` and completes
    it on `llm.compaction_done` (`llm.compaction_failed` falls back to
    trimming and is still followed by `compaction_done`).
  - Several failing passes stay one row. The row's reason holds only the
    language-neutral sizes ("77k → 24k"); duration is the time between the
    start and end events.
  - Ownership matches tool activity: on main only while main owns the
    conversation's open response (after Core's receipt); a chat-* thread
    belongs to its conversation. Running rows left by a lost feed or a
    restart are marked interrupted by the existing clean-up.
- The compaction events are added to the chat and main telemetry
  subscriptions. The server's live feed filters only by event name and
  thread prefix, so no server or Core change is needed.
- Frontend: the row shows "Compacting conversation memory…" (running, with
  a timer), "Conversation memory compacted" (done), "Couldn't compact
  conversation memory" (failed), with a compress icon. Its duration always
  shows, even with tool durations turned off. The hover text explains what
  happened and gives "Context 77k → 24k tokens". English, French, Spanish.

Not covered:

- Not seen against a live Core compaction; the Go tests feed recorded event
  shapes from prod telemetry (17:52, agent 11).
- A compaction row joins the surrounding tool group like any activity, so
  after it completes a group can read "Conversation memory compacted +2".
- If the sidecar restarts mid-compaction, the row is marked interrupted
  ("Conversation memory compaction stopped") rather than completed.

## Checks

- Go (`compaction_test.go`):
  - recovery: a non-compaction provider error is ignored; two failing
    passes stay one running row with a live frame; the retried `llm.start`
    completes it with "77k → 24k" and 40 s; a later `llm.start` changes
    nothing.
  - planned: `llm.start` inside planned compaction does not end it;
    `compaction_done` completes it with "90k → 12k".
  - failure: `llm.error` marks it failed.
  - main: background compaction before the message and while it is queued
    stays out of the conversation; the owned one is recorded and completed.
- Playwright `compaction.browser.ts` at 1280px and 390px: running label,
  timer counting from the start, breathing copy, compress icon; completed
  label with "79.0s", hover text with the sizes, sizes never shown as the
  label, survives reload, no sideways scroll. French failed label.
  Screenshots checked.
- Full Playwright suite: 190 passed. Bun: 148 passed (app root), 49
  (frontend). `tsc --noEmit` passed.
- `GOWORK=off go test -race -tags=integration ./...` passed (155.8s).
  `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
