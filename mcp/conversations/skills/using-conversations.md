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

Before noticeable tool work, send one short acknowledgement with
`phase=acknowledgement`, do the work, then send exactly one outcome with
`phase=final`. Between the two, send `phase=progress` only for a meaningful
achievement, plan change, blocker, or
request for input — never narrate individual tools, routine retries,
or unchanged waiting. The conversation is durable: deliver the final
outcome even if the user disconnected, and never repeat or paraphrase
a message whose send already succeeded.

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

The agent's main thread owns conversation discovery and creation, global
reports, and autonomous alerts. A conversation thread owns the visible reply,
history, approvals, and urgent local alerts for the conversation named in its
context. Main never writes an ordinary chat reply directly: it sends the result
to the originating conversation thread, which communicates with the person.

Generic workers never publish through Conversations. When spawning a worker,
do not grant the Conversations MCP or any `conversations_*` tool. Give it only
the domain tools it needs and require it to report milestones, blockers, and
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
   same conversation, so reusing a title is safe and correct.
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
normal final answers, or duplicates of chat messages. If an approval
or alert arises during a live conversation, also send one concise chat
message there explaining what needs attention.

## Approvals block until answered

The verdict arrives later as an `approval.result` event on the thread
that asked. Do not perform the gated action until it arrives. A denial
means do not do it — adjust or stop; never re-ask the same question
hoping for a different answer. The operator's note on the verdict is
them talking to you: honor it.

## Public conversations

A conversation marked audience "public" is a product's end user — a
site chatbot visitor, not an operator. There you only ever reply, with
`conversations_send`: courteous, on-topic, nothing internal. Alerts,
reports, and approvals are structurally refused in public
conversations. When a visitor request needs operator attention (a
refund over the limit, a decision, an incident), raise the approval or
alert in an OPERATOR conversation and mention the public conversation
id in it, then tell the visitor you are checking — the operator
decides in the inbox, and you relay the outcome.

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
