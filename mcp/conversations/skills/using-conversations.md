# Using Conversations

Conversations is the agent's one surface for people: dashboard chat,
the operator inbox (approvals, alerts, reports, status), and external
channels. Every inbox item IS a message in a conversation — acting on
a card anywhere updates every surface at once.

## Replying to people

Thoughts and plain assistant output are invisible. Only
`conversations_send` creates user-visible chat text. When a user
message arrives (an event prefixed `[chat]`), reply into that same
conversation — the id is in your thread's context. Do not answer
through another channel, a task note, or silence.

Images attached to an incoming message are provided directly as visual input.
Inspect them in that request; they may not be available on later model turns.
For a multi-step image task, identify the relevant visual fact before your first
acknowledgement and include it there, then use tools. For a simple question such as "What do you see?",
answer directly with `conversations_send`, `phase=final`. Do not send a separate
"I'll take a look" acknowledgement. Do not call an attachment-reading tool for
an image already supplied. Non-image attachments include a Conversations-scoped
attachment reference and may include a Storage file ID. Non-image attachments
also arrive as a server-backed file handle. When a receiving app's file,
document, or ZIP-import tool needs the original bytes, pass that handle
unchanged to its declared file input; the platform supplies the bytes. Do not
decode or inline the handle. Treat attachment IDs as scoped references, not
generic blob readers.
Distinguish file identification from content inspection: when the user asks
what file they attached, answer immediately from the filename, MIME type, and
size already in the event, with `phase=final`. Do not ask permission to look
for a reader. If the user asks for the file's contents, use a compatible
reader/import tool only when one is actually available in the current tool
catalog; if none is available, report that limitation with `phase=final` and
do not call `pace` or wait for a nonexistent tool. A file handle is metadata
and access to bytes, not extracted PDF or Office text.
When an image-generation or other compatible tool returns a `blobref://` image
handle, pass the complete handle in `conversations_send`'s `attachments` array
to publish it in the conversation. Conversations persists the reference and
authorized viewers can render it; do not download or base64-encode it first.
Describe only details you can actually see; the filename or byte count is not
evidence of image quality.

Before calling any work tool for a user request, including a single quick
lookup, call `conversations_send` with `phase=acknowledgement` alone. The
acknowledgement should briefly say what you are about to do. Wait for its
result before calling a work tool; never batch or parallelize the
acknowledgement with the work. Then do the work and send exactly one outcome
with `phase=final`. For short flows, including several related tool calls, keep working through to
the final outcome without a progress update. Tool count or a routine stage
boundary alone is not a reason to send an update. For longer work, complete a
substantial batch before sending a concise `phase=progress` update, and only
when meaningful work remains. Combine nearby milestones; normally leave about
a minute between updates unless the user needs to know about a blocker,
material plan change, or decision sooner. Do not narrate individual tool calls,
routine retries, or unchanged waits. If completion is near, finish and send the
final outcome instead. The
exceptions are a response you can give without a work tool, a simple image
question answered from the image already supplied, and a confirmation followed
only by a timed wait (see below). The conversation is
durable: deliver the final outcome even if the user disconnected, and never
repeat or paraphrase a message whose send already succeeded.

### Confirming a timed wait

When the user asks you to check or act later and your only next action is Core
`pace`, send one concise confirmation with `phase=final`, then call `pace`.
For example, "Check on it in five minutes" gets "I'll check in five minutes"
with `phase=final`, followed by `pace(sleep="5m")`. Do not label that
confirmation `acknowledgement` or `progress`: no work remains in the current
visible response while you wait. `phase=final` completes this response; it does
not cancel the later check or stop the thread. When the timer wakes, perform
the requested work and publish its outcome normally as a fresh response.
If scheduling requires a work tool, use the normal acknowledgement/work/final
sequence and confirm scheduling only after that tool succeeds. Do not keep a
response active merely because a future follow-up remains scheduled.

Write portable chat text: lead with the answer, use short paragraphs and
simple bullets or numbered lists, and avoid Markdown tables, raw HTML, or
transport-specific syntax. Basic emphasis, links, inline/fenced code, quotes,
and headings are adapted to each bound surface; Telegram receives native
Telegram formatting while the dashboard renders the stored Markdown. When
showing a specific supported entity, attach the matching component card in the
SAME send call as the text — never a second component-only message.

## Thread ownership and delegation

Threads are opaque identifiers; never infer a platform role from their name.
Conversations records which thread belongs to each conversation and enforces
that a bound conversation thread can operate only on that conversation.

Conversations owns the configuration of its conversation threads. This applies
to main as well as every child: never use Core `update`, `kill`, or a replacement
`spawn` to rewrite, rename, reconfigure, or replace a Conversations-owned thread.
The conversation thread must not use `evolve` to change its app-provided
instructions. Parent authority over ordinary workers does not transfer ownership
of an app-created conversation thread. Request app/operator configuration changes
when needed; do not repair a chat by changing its directive or tool allowlist.

Keep work that depends on the authenticated visitor inside the originating
conversation thread. Identity checks such as `partner_whoami` and user-scoped CRM
reads/writes depend on trusted thread context, not just possession of a tool.
Main and generic workers do not inherit that identity. Never turn the chat into
a forwarding stub, evolve main into a coordinator for its visitor requests, or
spawn a worker to bypass an identity failure. An `active external conversation
required` error means the call is in the wrong context: stop that attempt and
return the task to the original conversation thread. Do not supply, copy or guess
user IDs to make the call succeed. If the original thread also fails, report the
blocker through the normal escalation path without claiming the work succeeded.

The agent's main thread owns conversation discovery and creation, global
reports, and autonomous alerts. A conversation thread owns the visible reply,
history, approvals, and urgent local alerts for the conversation named in its
context. Main never writes an ordinary chat reply directly: it sends the result
to the originating conversation thread, which communicates with the person.

Generic workers never publish through Conversations. When spawning a worker,
do not grant the Conversations MCP or any `conversations_*` tool. Give it only
the domain tools it needs for work that does not require the visitor's identity
(for example, analysing already-authorized, non-sensitive data), and require it to report milestones, blockers, and
its final result to its parent. If a worker needs approval, it reports the exact
blocked decision to its parent; the parent requests approval and returns the
verdict. This is the same capability-ownership pattern used by Tasks.

## Where inbox items live: list, reuse, create

Every alert, report, and approval needs a `conversation_id` — there is
no default bucket. The flow:

1. **Reuse first.** If the item belongs to work an existing
   conversation asked for, use that conversation's id. Otherwise
   search your own conversations: `conversations_list` with `query`
   ("reports", "infra").
2. **Create deliberately.** No fit? `conversations_create` with a
   short, stable topic title: "Reports", "Infra monitoring", an
   incident name like "Certificate renewal — shop.example.com".
   Creation is title-idempotent: the same title always returns the
   same conversation, so reusing a title is safe and correct. If the list
   is empty, create one now; do not stop or substitute a thread id such as
   `main` for a conversation id.
3. **Titles name ongoing topics, never events.** Do not put
   timestamps, ids, counters, or per-item detail in a title —
   "Alert 2026-08-19" creates junk; "Infra monitoring" accumulates a
   history. One topic, one conversation, forever.

Keep one standing "Reports" conversation for periodic reports.
Incidents get their own named conversation so the operator can reply
into it and the dialogue stays on-topic.

## The inbox kinds — one global output surface

Alerts and reports are the agent's single operator-output surface. Main owns
global and autonomous alerts and every report. A conversation thread may raise
an urgent alert caused by work originating in that conversation, and main or
the originating conversation thread may request approval for work it owns.
Generic workers always report the condition or blocked decision to their
parent instead. The approval verdict returns to the asking main/conversation
thread. (Agent status lives in the status app, not here.)

- `conversations_alert` — a genuinely urgent or materially important
  problem: what broke, its impact, the next action. Severity honestly:
  `error` breaks things, `warn` degrades, `info` informs. One alert
  per condition — never re-alert an unchanged problem on every check;
  if it resolves, say so once in the same conversation.
- `conversations_report` — a digest across its period: concrete
  outcomes, evidence or metrics, blockers, next steps. Never a receipt
  for each action or an unchanged check. Follow the directive's report
  timing; otherwise at most one unsolicited report per day, and only
  when meaningful work occurred. Reports appear in their conversation
  and in the inbox.
- `conversations_request_approval` — only when work cannot continue
  without a human decision: state the exact decision, why, and the
  consequence of approving or denying.

Never raise inbox items for routine progress, ordinary failures,
normal final answers, or duplicates of chat messages. An approval card already explains the decision in the chat. Call
`conversations_request_approval` directly; do not first send a message saying
that you will ask for approval, or repeat the card with `conversations_send`.
For an alert, send a separate explanation only if it adds necessary context.

Omit `actions` for the standard Approve/Deny choices. Custom actions are an
array of `{id, label, style?}`; `id` is required, not `value`. Use unique IDs
other than `pending` or `resolved`. Styles are `primary`, `secondary`, or
`danger`. Put the proposed action first and the alternative second; without
styles the first gets an accent outline and the others stay neutral.

## Approvals block until answered

The verdict arrives later as an `approval.result` event on the thread
that asked. Do not perform the gated action until it arrives. A denial
means do not do it — adjust or stop; never re-ask the same question
hoping for a different answer. The operator's note on the verdict is
them talking to you: honor it.

Acknowledge the verdict in the originating conversation before continuing.
When main requested the approval, use `conversations_send` with
`phase=acknowledgement` and `approval_message_id` from the verdict event.
This permits one receipt for main's own resolved approval, including a denial;
it does not permit ordinary replies from main or imply the action succeeded.

## Public conversations

A conversation marked audience "public" is a product's end user — a
site chatbot visitor, not an operator. There you only ever reply, with
`conversations_send`: courteous, on-topic, nothing internal. Alerts,
reports, and approvals are structurally refused in public
conversations. When a visitor request needs operator attention (a
refund over the limit, a decision, an incident), send parent/main one
escalation containing the public conversation id, requested decision, and
reply thread. Main finds or creates an OPERATOR conversation and raises the
approval there, then sends the decision back to the originating thread.
If `conversations_list` finds no suitable operator conversation, main must
call `conversations_create` with a stable topic title such as "Refund approvals"
and use the returned id for the approval or alert. An empty list is not a
blocker, and a public conversation is never a fallback destination. Main is
the coordinator itself; it must not try to send an escalation to `main`.
The public thread must never create, list, or write to another conversation.
Tell the visitor you are checking and relay the decision when main replies.

## Reading context

- `conversations_history` — the transcript, for joining mid-way or
  recalling what was said. A conversation thread reads only the exact
  conversation in its context.
- `conversations_list` — main lists the conversations the agent participates
  in. A conversation thread already has its authoritative id.

## Rooms and Telegram

A conversation can hold several agents: unaddressed user messages go to
the lead, `@name` addresses one participant, and `@all` addresses every
participant. Telegram `/agents` lists the available names. Any participant may send. If you are not the lead, contribute
when addressed or when your expertise is the point — do not echo the
lead. Telegram chats remain bindings to ordinary conversations even when
pairing or public intake created them automatically; agents always reply with
`conversations_send`. Do not ask for bot tokens, numeric chat ids, user ids, or
Telegram credentials—the guided transport setup and platform integration
connection own those details. Public Telegram conversations keep the same rule
as public web conversations: reply to the visitor, never place approvals or
internal alerts in front of them. `/new` changes the active Telegram route but
does not erase the previous Conversations transcript.
