# Conversations v0.27.0 validation — 2026-11-21

Main routing and conversation thread lifecycle. Conversations app only: no
Core, server or SDK change (App SDK stays v0.115.2, Apteva >= 0.40.6).

## Behavior

- An agent's first qualifying operator chat (one agent, web origin, operator
  audience, no transport binding, no external participant) binds to `main` in
  the same transaction as its first inbound message. A unique partial index
  allows one per agent. Existing bindings never move, so on upgrade the agent's
  next new chat takes main. No label, flag or setting.
- Messages go to main with `SendTrackedAgentEvent`, the stable event id, the
  accepted/duplicate receipt rule and a short reply contract. Main is never
  ensured, spawned or reconfigured. Main stays a global caller; it may send
  ordinary replies only in the conversation it answers.
- Main's background turns no longer show as Thinking in that conversation.
- Delete, archive, transport binding or an external participant releases main.
  Main is never retired or killed.
- Chat-thread directive: explicit pacing decision every turn, report-back of
  outcomes/decisions/agent-wide instructions to main with `send id=main`.
- Deleted conversations' threads are killed with backoff (including threads
  retired by earlier releases). Idle threads (default 14 days, `idle_thread_days`)
  get one check; `conversations_keep_thread` keeps them; otherwise main gets a
  dormancy note and the thread is killed, then recreated with a resume notice.

## Checks

- 12 new unit tests in `main_routing_test.go` (routing, eligibility, global
  main, no main reconfiguration, release on delete/archive/bridging, kill and
  retry, idle check/keep/sleep/resume, exclusions, streamer, directive). Two
  were confirmed to fail when their guard is removed.
- New real-sidecar test `TestSidecar_FirstChatIsAnsweredByMainWhichStaysGlobal`.
- Existing chat-thread tests now give main its conversation first
  (`occupyMainRouting`, and in one sidecar test an explicit first chat), so they
  keep exercising `chat-*` threads.
- `GOWORK=off go test -race -tags=integration ./...` passed (142.4s);
  `go vet` clean, including `-tags 'integration scenario'`.
- 142 Bun UI/client tests and 178 Playwright browser tests passed; package
  build, consumer TypeScript check, app-served frontend and four panel
  build/import checks passed. `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- Tier 3 (real-model scenarios) was not run: no Codex test credentials on this
  machine. The image scenario now calls `claimMainRouting`, which also requires
  a real reply from main; it compiles but is unrun.
