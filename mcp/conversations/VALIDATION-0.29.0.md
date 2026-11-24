# Conversations v0.29.0 validation — 2026-11-24

Operators could not tell which model answers a conversation, or whether the
agent answers from its main thread or the conversation's own thread.

Change:

- The streamer records, per agent and thread, the provider and model from
  Core's `llm.start` (sent before each call, so it precedes the reply the
  call produces) and `llm.done` (names the provider that actually answered,
  including a fallback). A primary `llm.provider_error` clears the record:
  until the fallback reports, the model behind the next reply is unknown.
  Both feeds (chat threads and main) now also subscribe to `llm.done` and
  `llm.provider_error`; those two events only update this record and never
  touch response progress.
- `appendAndDeliver` stamps every agent message (send, approval, report,
  alert) with that record. Migration 018 stores it in a new
  `messages.generated_by` column. The column is not in the message select,
  so message payloads (dashboard, exported chat, Telegram, mobile) never
  carry it, and it is not part of the retry fingerprint.
- `GET /participants` adds `agents`: per agent participant, the bound thread
  and the provider/model, thread and time of the agent's latest reply that
  recorded a model. Delegated (visitor) callers still cannot read
  `/participants`.
- Details shows, under each agent, the model name on one line and
  "Main thread"/"Own thread" · provider on the next. The full model name,
  thread id and reply time are in the hover title. With no recorded reply:
  "Model not recorded yet".

Not covered:

- Replies sent before 0.29.0 have no record; Details shows "Model not
  recorded yet" until the agent replies again.
- A project-scoped install never receives telemetry from agents without a
  project (the Apteva Helper), so their replies record no model there.
- This is the model of the last reply, not the thread's configured model;
  reading a thread's configuration would need a new SDK call.
- A main thread is shared: Details shows the model main used for its last
  reply in this conversation.

## Checks

- `TestRepliesRecordTheModelThatProducedThem`: before any reply,
  `/participants` shows the bound `chat-<id>` thread and no model; another
  thread's `llm.start` does not leak into this conversation; the reply
  records `{"provider":"anthropic","model":"claude-a"}`; the stored
  message's JSON contains neither the model nor `generated_by`; after a
  primary provider error the next reply records nothing and Details keeps
  the previous model; after the fallback's `llm.done` the next reply records
  it and Details shows it. With the stamping line removed, the test fails
  (`first reply generated_by=""`).
- `TestRetriedReplyStaysADuplicateWhenTheModelChanges`: the same tool call
  retried after a model change returns the original message
  (`inserted=false`) and keeps its original model. With the model folded
  into the fingerprint, the test fails (`retry was not a duplicate`).
- `TestMainThreadModelIsTrackedWithoutAnOpenResponse`: main's model is kept
  per agent even when main is not answering a conversation; a `llm.done`
  without a model keeps the last one.
- Playwright: 183 passed, including the new
  `details-model.browser.ts` (own thread/OpenAI, main/Anthropic, and no
  reply, with the hover title).
- Example host preview at 1280px: checked all three Details states.
- `GOWORK=off go test -race -tags=integration ./...` passed (179.3s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- 148 Bun UI/client tests passed (2,078 assertions); consumer TypeScript
  check passed.
- App SDK stays pinned at v0.115.2.
