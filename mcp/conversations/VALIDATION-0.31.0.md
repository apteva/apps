# Conversations v0.31.0 validation — 2026-11-24

On prod (Services, agent 190, its main-answered "Messaging" chat), a request
to read 8 CRM inbox threads, then mark 3 as spam and close 3 after approval,
produced 4 spawned helper threads for about 10 quick CRM calls. Core's main
prompt says to "assign the units to focused owners instead of executing the
first unit on main" and keeps only "very small work" local. Core is unchanged.

Change (prompt text only; no API, data or UI change):

- `mainReplyContract` (appended to every chat message delivered to main) now
  says the person is waiting, a request made in the conversation is very
  small work done in this turn even across several items, independent calls
  run in parallel, and spawning is only for work continuing after the reply
  or too large for one turn.
- `skills/using-conversations.md` adds "Do conversation requests yourself"
  for main and conversation threads, and scopes Core's delegate-independent-
  units rule to standing and background work.

## Checks

Live, `apteva test --tier 3 --provider openai-codex --model gpt-6.2-sol`:

- New `scenarios/28-main-does-requested-work.yaml`: main answers its first
  operator chat (driver asserts the bound thread is `main`), reads three todo
  lists, names the vendor pitches and completes both Bounced emails todos,
  leaving the others open; `tool_not_called: spawn`.
  - 0.30.1 wording (same scenario copied onto the 0.30.1 tag): 2 of 3 passed;
    1 failed with 3 spawned threads (`triage-*`), 15 iterations, 64s.
  - 0.31.0 wording: 4 of 4 passed, no spawns, 6 iterations, 38-44s.
- Regressions on 0.31.0 wording, all passed with no spawns: 01 chat round
  trip, 07 approval round trip, 13 approval destination, 14 conversation
  ownership, 15 long multi-step progress, 24 work-conversation inbox.
- 18 short multi-step quiet: failed once (the model called `todos_list`
  twice, all and done, against an exact count of 1; the driver itself passed
  in 32s and the runner then waited to its 12-minute timeout), then passed on
  0.31.0 and on 0.30.1 in a paired rerun. Treated as model variance; watch
  for repeats.

Local:

- `TestMainReplyContractKeepsRequestedWorkOnMain` and the skill fragment
  checks in `manifest_test.go` pin the new text.
- `GOWORK=off go test -race -tags=integration ./...` passed (155.9s); `go
  vet` clean including `-tags 'integration scenario'`; `GOWORK=off go mod
  tidy` left go.mod/go.sum unchanged.
- 148 Bun tests and 187 Playwright tests passed (bundles rebuilt for the
  version bump only).
- App SDK stays pinned at v0.115.2.
