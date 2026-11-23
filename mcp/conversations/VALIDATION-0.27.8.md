# Conversations v0.27.8 validation — 2026-11-23

Main routing (0.27.0) gives an agent's first one-agent operator chat to the
agent's own main thread. The Apteva Helper has no project and serves every
project, so its main thread is shared platform-wide. Its first chat in a
project still took main: prod conversation conv-b9d4… (moved to a chat thread
by hand) and local install 1765's conv-b15d… (still on main).

Change:

- Migration 017 adds `conversations.main_routing` (default 1, so existing
  conversations keep their behavior).
- `POST /chats` already lists the project's agents to validate the request.
  The server includes the Helper in that list, for a global install, with an
  empty `project_id` (`apps_callbacks.go`, `handleCallbackAgentList`). When the
  lead agent's entry has no project, the conversation is created with
  `main_routing=0`.
- `mainRoutingEligibleTx` reads the column with the rest of the conversation
  row and refuses main when it is 0. Sending a message makes no platform call
  for this; the decision is made once, at creation.
- Other creators keep the default. None of them can produce a main-routed
  Helper chat: inbox (origin `app`), agent tool (origin `agent`) and Telegram
  onboarding (origin `telegram`, public, external participant) conversations
  never qualify for main.

Not covered:

- A Helper chat created before 0.27.8 with no message yet can still take main
  on its first message (`main_routing` defaults to 1). Conversations already
  bound to a thread keep that binding.
- On prod the Helper's Core is paused instance-wide, so a Helper chat thread
  there will not answer until that is resolved. That is separate from routing.

## Checks

- `TestProjectlessLeadNeverTakesMainRouting`: chats created through `POST /chats`
  with a project-less lead are stored with `main_routing=0`, bind their first
  message to `chat-<id>` (one ensure for that thread, no main event), and a
  second Helper chat does the same; no main binding exists for the Helper. An
  agent with a project, created the same way, still takes main. With the new
  check removed, the test fails with `helper chat thread="main"`.
- `GOWORK=off go test -race -tags=integration ./...` passed (160.1s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- 148 Bun UI/client tests passed, with 2,066 assertions; package build,
  app-served frontend build and consumer TypeScript check passed. The rebuilt
  app bundle is byte-identical (version literals only).
- App SDK stays pinned at v0.115.2.
