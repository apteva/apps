# Conversations v0.27.3 validation — 2026-11-21

v0.27.2 matched main's tagged Core receipt (`[console] [chat] ...`) but did not
fix the live symptom: after upgrading local :5280, the response to a new
message in the main-routed conversation still stayed `thinking` after main's
final reply. The receipt never arrived: the app subscribes to Core telemetry
with `ThreadPrefix: "chat-"`, so no event from `main` ever reached the
streamer. With the chat feed connected, the response waited for a receipt that
could not come, and `finishResponse` skips settlement while a receipt is
pending.

Fix:

- A third telemetry feed for `main` (same response events as chat threads),
  with an exact `thread_id == "main"` check since the server filters by prefix.
- Main's events are attributed in memory (no DB lookup per token chunk) to the
  agent's receipt-gated response, the conversation main answers. Approval
  receipts on main are ungated and never take these events.
- Tool activity from main is recorded only for calls started inside that owned
  response; results update only rows recorded so. Background work never
  appears in the conversation.
- Response checks use the feed covering the response's thread
  (`feedConnectedLocked`). Without main's feed, main's responses register no
  receipt preview and settle on main's durable reply. The end of main's feed
  interrupts only main's running activity rows and leaves the chat feed alone.

The server needs no change: app telemetry reads accept any thread prefix.

## Checks

- New `TestMainReplySettlesWithoutMainTelemetry` reproduces the live
  condition (chat feed connected, no main events). With v0.27.2's global
  `telemetryConnected` checks it fails with "Thinking stayed on after main's
  final reply"; it passes now. Reverting only one of the two checks still
  passes: both layers settle main's response.
- New `TestMainToolActivityOnlyInsideOwnedResponse`: background tool calls
  before the message, while it is queued and after the reply never create
  rows; the call made inside the owned response is recorded and completed by a
  result that lands after the final reply. Without the ownership gate the
  background calls leak into the conversation and the test fails.
- New `TestMainFeedEndFallsBackWithoutTouchingChatFeed`: a `maintenance`
  thread on the main prefix is skipped, and the end of main's feed clears only
  main's connected flag.
- `TestMainRoutedReplySettlesThinking` (v0.27.2) now runs with main's feed
  connected, as in production.
- `GOWORK=off go test -race -tags=integration ./...` passed (165.9s); the main
  routing and streamer tests passed five times in a row under `-race`. `go vet`
  clean, including `-tags 'integration scenario'`. `GOWORK=off go mod tidy` left
  go.mod/go.sum unchanged.
- 142 Bun UI/client tests passed, with 2,044 assertions; package build and
  consumer TypeScript check passed. Frontend source unchanged (version literals
  only), so Playwright was not rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here.
- App SDK stays pinned at v0.115.2.
