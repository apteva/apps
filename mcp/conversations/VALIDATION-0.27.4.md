# Conversations v0.27.4 validation — 2026-11-22

Reported on local :5280: the Apteva Helper (agent 1, no project) could not
answer its chat. Telemetry for 2026-11-22 10:10:49 UTC shows main receiving
"[console] [chat] Hi", calling `conversations_send` for
`conv-b15d16e4dc9733284ae4cef5`, the call failing in 3 ms, and Core settling
the event at 10:10:52.96 without a reply. `requireAgentCaller` refused any
caller without a project, so a project-less agent could never write. The
response then stayed `thinking`: the only things that settle it are a durable
reply, an error, or a pace/done call, and main's turn ended with none of them.

Fix:

- `requireAgentCaller` needs an agent and a thread, not a project.
  `requireParticipant` authorizes a project-less caller by its participant
  row (only project users add agents) and then runs the call in the
  conversation's project. Callers with a project still get "conversation not
  found" for another project's conversation.
- The thread-binding and retired-thread lookups use any project when the
  caller has none, before the conversation's project is adopted. A Helper
  conversation thread therefore stays confined to its conversation across
  projects, and a retired one stays refused.
- `conversations_list` and `conversations_create` require a project and say
  why.
- `event.settled` joins both response feeds. When its `source_event_id` is
  `conversation:<id>:message:<n>:agent:<a>` and matches the open response
  (same agent, thread and message), the response and its acknowledgement
  settle. Core emits it at the thread's wait boundary, after pending async
  tools finish. A failed tool call alone does not settle: the model may retry.
- Responses wait for Core's receipt only for agents the feeds have shown at
  least once. A project-scoped install never receives a project-less agent's
  telemetry, so those responses settle on the durable reply.

Agents can still only be added by users with project access
(`sdk.ListAgentsVia(projectID)` on the roster routes); agents have no tool
that adds participants. Unchanged in this release.

## Checks

- New `projectless_test.go`:
  - a project-less participant replies from main, reads history, and posts a
    report that lands in the conversation project's inbox; Thinking settles;
  - a project-less non-participant gets "not a participant" and writes
    nothing;
  - a caller with a project gets "conversation not found" for another
    project's conversation;
  - project-less `conversations_list` and `conversations_create` are refused
    and create nothing;
  - a project-less conversation thread can write to its own conversation, is
    refused on a conversation in another project ("belongs to conversation"),
    and after its conversation is deleted is refused as retired;
  - after a failed `conversations_send`, the response stays open until
    `event.settled` for that message arrives; settled events from another
    agent, for an idle check, for another message, or naming another agent do
    not settle it or its acknowledgement;
  - an agent the feeds never showed settles on its reply with both feeds
    connected; once seen, a reply before Core's receipt still keeps Thinking;
  - `parseConversationThreadEventID` round-trips and rejects other tracked
    event IDs.
- Mutation checks, each restored afterwards. Each was caught by the new tests:
  adopting the conversation's project before the binding check; exact-project
  thread-binding lookup in the store; exact-project retired lookup; ignoring
  `event.settled`; skipping the message-id match (caught through the
  acknowledgement); skipping the agent match; removing the seen-agent gate;
  forcing it off.
- `GOWORK=off go test -race -tags=integration ./...` passed (161.8s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- 142 Bun UI/client tests passed, with 2,044 assertions; package build and
  consumer TypeScript check passed. Frontend source unchanged (version literals
  only), so Playwright was not rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here.
- App SDK stays pinned at v0.115.2.
