# Conversations v0.33.0 validation — 2026-11-25

On prod (personal project, Messaging Agent 197) two daily summaries went out a
minute apart (main 16:59, chat thread 17:00). Main had created "Email triage"
(origin `agent`) on 23 Nov for its reports. The person's first message there,
"Thanks", bound the conversation to a new `chat-conv-…` thread, because main
routing accepted only conversations started in the web UI. That thread read the
agent's instructions, and the chat-thread rule "Keep recurring work that belongs
to this conversation here" made it start its own hourly inbox job and daily
summary.

Change:

- `main_routing.go`: origin `agent` is eligible for main, with the same other
  conditions (direct, operator audience, lead is the agent, no transport, no
  external or other agent participants, main_routing allowed). Only main and
  unbound workers can call `conversations_create`. One main conversation per
  agent still holds; existing bindings are never moved.
- Main's reply rule adds, for agent-created conversations only: main created
  the conversation for its own reports, should answer from what it knows, and
  its standing jobs keep running from main unless the person asks for a change.
- `threads.go`: the chat-thread directive no longer says to keep recurring work
  that belongs to the conversation. It says standing duties from the agent's
  instructions (schedules, recurring reports, inbox checks, monitoring) belong
  to main; a thread never starts, repeats or takes them over, and starts
  recurring work only when a person asks for it in the conversation. The
  directive change alters the profile hash, so running threads are reconciled
  on their next inbound message.
- Skill `using-conversations.md` and the manifest description say the same.

Not covered:

- No live model run. The tests check routing, the text main receives and the
  directive; whether a model follows the new wording is not measured.
- A thread that already scheduled its own wake keeps it until its next message
  or until it is stopped. On prod, the Email triage thread was retired and the
  conversation moved to main by hand (see below).

## Checks

- Go (`main_routing_test.go`):
  - `TestAgentCreatedConversationRoutesToMain`: `conversations_create` from
    main, then a user "Thanks": bound to main, no thread ensured or spawned,
    one tracked event to main carrying the message, the reply rule and the
    "You created this conversation yourself" note. A second agent-created
    conversation gets its chat thread, whose ensured directive carries the
    standing-duty rule.
  - `TestMainReplyContractNotesOnlyAgentCreatedConversations`.
  - `TestChatThreadDirectiveLeavesStandingDutiesToMain`, including that the
    old "Keep recurring work…" sentence is gone.
  - `TestMainRoutingSkipsPublicRoomAndBridgedConversations` no longer lists
    the agent-created conversation as ineligible.
- `GOWORK=off go test -race -tags=integration ./...`, `go vet` (also with
  `-tags 'integration scenario'`), Bun 148 (app root) and 49 (frontend).
