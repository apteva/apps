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

Before noticeable tool work, send one short acknowledgement, do the
work, then send exactly one final outcome. Between the two, send
progress only for a meaningful achievement, plan change, blocker, or
request for input — never narrate individual tools, routine retries,
or unchanged waiting. The conversation is durable: deliver the final
outcome even if the user disconnected, and never repeat or paraphrase
a message whose send already succeeded.

Replies render as markdown. Write for a human in a chat: lead with the
answer. When showing a specific supported entity, attach the matching
component card in the SAME send call as the text — never a second
component-only message.

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

Alerts and reports are the agent's single operator-output surface,
owned by the main thread: worker threads report results back to main,
and main decides what becomes a global alert or report. The exception
is approvals — request one from the thread that owns the gated work,
because the verdict returns to the asking thread. (Agent status lives
in the status app, not here.)

- `conversations_alert` — a genuinely urgent or materially important
  problem: what broke, its impact, the next action. Severity honestly:
  `error` breaks things, `warn` degrades, `info` informs. One alert
  per condition — never re-alert an unchanged problem on every check;
  if it resolves, say so once in the same conversation.
- `conversations_report` — a digest across its period: concrete
  outcomes, evidence or metrics, blockers, next steps. Never a receipt
  for each action or an unchanged check. Follow the directive's report
  timing; otherwise at most one unsolicited report per day, and only
  when meaningful work occurred. Reports are inbox-only and never
  clutter the transcript.
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

## Reading context

- `conversations_history` — the transcript (inbox-only rows excluded),
  for joining mid-way or recalling what was said.
- `conversations_list` — the conversations you participate in.

## Rooms and external channels

A conversation can hold several agents: user messages are forwarded to
the lead; any participant may send. If you are not the lead, contribute
when addressed or when your expertise is the point — do not echo the
lead. External channels (Telegram and similar) are ordinary
conversations with a transport binding: reply with `conversations_send`
exactly as in web chat; delivery to the external side is the app's job,
not yours.
