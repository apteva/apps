# Conversations v0.27.2 validation — 2026-11-21

After v0.27.0, the conversation list kept its working dot (and the transcript
its Thinking state) after main's final reply in the conversation it answers.
Seen live on local :5280: the response for the user's "Hey" stayed `thinking`
on `main` four minutes after main's reply landed.

Cause: an inbound response hands progress to the agent only once Core's
`event.received` preview starts with `"[chat] " + content`. Chat threads
receive bus events whose text starts that way. Main receives tracked agent
events, which Core records with `source: console` and the text
`"[console] [chat] Hey ..."`. The match failed, so the response stayed queued:
later events were ignored and the final reply's settlement was skipped.

Fix: when the plain match fails, strip the `[<source>] ` tag Core reports and
compare the rest, or, when Core cut the text at 100 bytes, the bytes it kept
(with a split trailing rune dropped). The v0.27.0 resume notice for a dormant
thread was prepended and had the same effect; it now follows the message.
Go only: no migration, tool, skill or frontend change.

## Checks

- New `TestMainRoutedReplySettlesThinking` delivers a short, a long (>100
  bytes) and a split-Unicode message to main, feeds Core's tagged and cut
  preview, checks that an unrelated console event cannot take over the
  response, then sends main's final reply: no conversation stays active. With
  v0.27.1's plain prefix match all three cases fail with "Thinking stayed on
  after main's final reply", the live symptom.
- `TestIdleThreadIsCheckedThenSleptThenResumed` now requires the resumed event
  to start with `[chat] `; it fails with the v0.27.0 prepended notice.
- `TestStreamerIgnoresMainBackgroundTurns` settles main's response through
  `finishResponse` instead of deleting it by hand (which had hidden this bug).
- `GOWORK=off go test -race -tags=integration ./...` passed (156.3s); `go vet`
  clean, including `-tags 'integration scenario'`. `GOWORK=off go mod tidy` left
  go.mod/go.sum unchanged.
- 142 Bun UI/client tests passed, with 2,044 assertions; package build and
  consumer TypeScript check passed. Frontend source is unchanged (version
  literals only), so Playwright was not rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here.
- App SDK stays pinned at v0.115.2.
