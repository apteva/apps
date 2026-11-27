# Changelog

## 0.37.0 — 2026-11-27

- When the model writes several tool calls in one response, a call it has finished writing now shows as "Ready" until it runs, while the call still being written shows "Writing". Before, only the call being written had a row: a finished call vanished when the model moved on and came back when the whole response ended. A grouped row reads, for example, "1 writing · 1 ready".
- Uses Core's new `llm.tool_ready` event (apteva/core#19). With an older Core nothing changes: calls go from Writing to Running as before.
- Ready calls are cleared when the model starts a new pass and when Core retries a failed request on another provider (`llm.provider_error`, which comes without a new `llm.start`), since calls from a failed stream never run.

## 0.36.0 — 2026-11-27

- While the model writes a tool call, the row says what it's doing instead of "Preparing a tool action…". Conversations reads Core's `_reason` argument out of the streamed argument chunks and shows it as soon as it's complete; models usually write it first (on local, "Counting project agents" showed 141ms into a 297ms write). Calls whose reason comes later still show the generic label until then.
- The state is now called "Writing" (it's the model writing the call's arguments), and writes over 1 KB show their size ("1.5 KB"), so a long write visibly moves. Prod measurement behind this: 16% of Codex tool calls spent over 3s in this state, driven by argument size (median ~1.5 KB at ~250 B/s), not by Core or the app.
- Size updates are throttled (a frame per 256 bytes, at most every 500ms); argument text is never sent to clients, only the label and the byte count.

## 0.35.1 — 2026-11-26

- A queued message now shows where it was sent to the agent, not where it was typed. A message queued while the agent was still working rendered above the reply it had waited for, because that reply was saved after the message was queued (prod: the "instagram reference" message showed above "Done. Server Tycoon…"). A release or steer records the last message shown at that moment (`placed_after`, migration 020), and the transcript places the message right after it. Ids, paging and deliveries are unchanged.
- A release now reaches open chats and the changes feed straight away. Before, only the creation was logged, and the chat picked up the released state from a later refetch.

## 0.35.0 — 2026-11-25

- Alert, approval, report, question and form cards are redesigned to read like messages instead of full-width banners. They are capped at message width on desktop and sit on the host theme's card surface, radius and shadow, so they follow all four dashboard themes (terminal/clean × dark/light). A thin colored edge marks severity or a pending decision.
- Card text is rendered as markdown. An alert's bold lead-in or heading becomes its title, and a trailing `Source:` line moves to the footer, next to the time and a Dismiss button.
- Severity labels are sentence case and localized (Info, Warning, Error, Critical).
- Approvals show "Approval needed" while pending. The optional note sits behind "Add note".
- Long reports fold behind "Show full report", and report section headings are body-sized.
- Inbox rows show plain-text titles instead of raw markdown.

## 0.34.5 — 2026-11-25

- Thinking no longer stays on after a finished reply. When the agent replied and then paced to wait for new messages (`clear_wake`), Core's last model call of that run reopened Thinking, nothing closed it, and the operator's next message was queued behind it (prod Helper chat). A pace now only lets a later model call count as new work once its timer is due; `clear_wake` never does, and a new message always does.
- Tier 3 scenario 30 (reply, then pace) covers it end to end with a real model; Go tests replay the prod events exactly.
- Tests: the answered-form test no longer fails under `-race` (its events were dated before the answer opened the response). Scenario 21 no longer requires `code_read_file`; gpt-6.2-sol reads files with `repos_run_command`.

## 0.34.4 — 2026-11-25

- Answering a form or question card now shows the agent's work like a typed message does: Thinking while it works and a tool row for every call. The answer opened no live response, so nothing showed until the final reply, and on main every tool call was dropped (prod: 15 of 16 archive calls missing after a form). The response opens before the answer is sent, gated on Core's receipt; a failed or duplicate delivery closes it, and a response already running in the conversation is kept.
- Tier 3 scenario 29 (form answer → live work) covers it end to end with a real model.

## 0.34.3 — 2026-11-25

- A helper's finished model turns no longer look busy. They show a still mark and read "Thought for 2.1s"; the moving dots and "Thinking · …" are kept for a turn that is still running.
- Test host: the app-card tests serve the built panel bundles unchanged. Re-bundling the minified output let Bun 1.3.13 rename a parameter onto the name it shadows, failing the tests on code the dashboard never runs.

## 0.34.2 — 2026-11-25

- A finished helper's steps no longer end with a stray "Thinking · 1.0s · Ended with the helper" after its report. That row was the model turn that called `done`: the `done` call is hidden and the helper ended with it, so it is now left out. Seen on dev with demo-helper.
- "Waiting 60 seconds" shows how long the helper actually waited. `pace` and `wait` return at once (13 ms), so their time is now measured to the helper's next step, or counts up while it is still waiting.

## 0.34.1 — 2026-11-25

- Keep one helper card per helper. Core can list a new helper a moment before it reports which spawn call started it. 0.34.0 stored that first sighting under its start time, then stored the helper again once the spawn was known and marked the first copy "Stopped without finishing" (seen live on dev). The first row is now kept and updated.
- Helper steps take the model and result from what the helper actually did: the header shows the model of its last turn (the thread list only knows the tier, such as "large"), and the result falls back to the text the helper ended with. The helper's own `send` and `done` calls are no longer listed, since "Reported to main: …" and the result already show them. A helper with no thread name is named by its purpose, which is no longer repeated under it.

## 0.34.0 — 2026-11-25

- See what an agent's helpers are doing. When an agent spawns a thread to do part of the work, the spawn row in the conversation used to stay a one-line "Start a helper" entry while the helper worked unseen. Once the platform reports the spawned thread, the row becomes a helper card: its name (thread name, then purpose, then the spawn reason), what it is doing now ("Using web search", "Thinking", "Sleeping until 14:05") with the app icon of the current tool, a running timer, and "+n helpers" when it started helpers of its own. Expanding it shows the purpose, model and start time, the last eight steps oldest first ("Show earlier" for the rest), "Thinking · 2.1s" for model turns, "Reported to main: …" for its reports, "Ended with the helper" or "Stopped without finishing" for steps cut short, its own helpers as nested cards (two levels), and the result with "Show full". Each tool step folds its arguments and output behind "Details". The card stays after the reply as a record; a helper finishing adds no unread count or notification.
- While a reply is open and the thread answering is waiting on helpers it started, the chat says "Waiting for 1 helper…" instead of "Thinking…".
- Details has an **Agent work** section under Participants: per agent, main with its state and next wake, the agent's conversation threads (named after their conversations, linked, this one highlighted), its helpers with what they are doing, threads that ended in the last 24 hours (folded), and a banner when the agent is paused or its model calls keep failing. It refreshes every 10 seconds while shown.
- "Show helper steps" in Tool display opens helper cards expanded. Off by default.
- Operators only. Public (visitor) conversations get no helper cards, no steps and no Agent work, and spawns there are never linked. Labels in English, French and Spanish; icons are SVG; finishing is announced politely to screen readers; animations stop with reduced motion.
- How it works: the spawn row now keeps Core's tool span and requested thread id (migration 019), and a new `conversation_workers` table links each spawned thread to the conversation through the topology's `spawned_by` span (nested helpers inherit their parent's conversation). One SDK thread watcher runs per agent only while it has unfinished linked helpers or just spawned one, capped at 50 live watchers (agents past the cap poll every 15 s); watchers resume after a restart, and a helper that disappears from the list is marked "Stopped without finishing". New routes `GET /workers`, `GET /workers/activity` (steps, with arguments and results for operators) and `GET /agent-work`, and a `worker` stream frame. Requires app-sdk v0.122.1. On platform servers without thread observation the spawn row stays as before.

## 0.33.0 — 2026-11-25

- A conversation an agent created for its own reports is now answered by the agent's main thread when someone replies there. Before, only chats started in the dashboard could go to main, so a reply in an agent-created conversation got its own conversation thread. On prod, main created "Email triage" for its daily inbox summaries. A "Thanks" there gave the conversation its own thread, which read the agent's instructions, started its own hourly inbox job and sent a second daily summary a minute after main's. Main now gets the reply with a note that it created the conversation and that its standing jobs keep running where they are. Main still answers one conversation per agent; existing bindings are not moved.
- Conversation threads no longer take over the agent's standing duties. Their instructions used to say to keep recurring work that belongs to the conversation in the thread. They now say that schedules, recurring reports, inbox checks and monitoring from the agent's instructions belong to main, and that a thread starts recurring work only when a person asks for it in that conversation. Running threads pick up the new instructions on their next message.

## 0.32.0 — 2026-11-24

- Show when an agent is compacting its conversation memory. When a thread's context gets too long, Core summarizes older parts before the next model call. Emergency recovery after an over-budget request can take a minute or more with no tool calls and nothing to stream, so the chat showed a bare "Thinking…" and looked stuck (79 s on prod). The chat now shows "Compacting conversation memory…" with a running timer while it happens, and keeps a quiet "Conversation memory compacted · 79.0s" line afterwards. Hovering it explains what happened and shows the context size before and after ("77k → 24k tokens"). If compaction fails with the turn, the line says "Couldn't compact conversation memory".
- It is an activity row, not a message: no unread count, no notification, nothing the agent reads. It follows the same ownership as tool activity, so compaction during an agent's background work on main never appears in the chat main answers. Labels are translated (English, French, Spanish). It uses events Core already emits (`llm.provider_error` class `context_length_exceeded`, `llm.context_recovery`, `llm.compaction_started`, `llm.compaction_done`); no Core or server change is needed.

## 0.31.0 — 2026-11-24

- Agents now do the work requested in a conversation themselves instead of spawning helper threads for it. Core tells an agent's main thread to hand independent units of work to separate threads. When main answered its operator chat, a quick request such as "read these threads, mark three as spam and close three" turned into four helper threads for about ten CRM calls, which added delay and hid the work behind "Assigning…" rows. The reply rule main receives with each chat message, and the Conversations skill for main and conversation threads, now treat a request made in the conversation as small work done in the same turn (in parallel when the calls are independent). Spawning stays for work that has to continue after the reply (waiting, polling, retries, a schedule) or is too large for one turn.
- New live scenario 28 (`main-does-requested-work`) fails if main spawns while reading three lists and completing two todos.

## 0.30.1 — 2026-11-24

- Stop code block headers covering what is above them. 0.30.0 painted a 16px strip of page background above every code block header, meant to hide text scrolling past a stuck header. It was painted all the time, so it hid the block's top border and rounded corners and cut off the bottom of the line above the block. The strip now appears only while the header is stuck at the top of the transcript (browsers with scroll-state container queries: Chrome and Edge 133+); elsewhere nothing is painted.

## 0.30.0 — 2026-11-24

- Make code blocks in messages easy to read and copy. Agents put text meant to be pasted elsewhere (prompts, commands, configs) in fenced code blocks, which showed as a plain box: no copy button, prose cut off at the right edge behind a sideways scrollbar at the very bottom, and no height limit. Each block now has a header with its language ("Text" when the fence names none), a wrap toggle and a Copy button that copies exactly what the agent wrote and confirms with "Copied". Prose (unlabelled, text and Markdown blocks) wraps; code keeps its lines and scrolls sideways. Blocks over 18 lines or 1,400 characters start collapsed behind "Show all N lines". The header stays at the top of the transcript while you scroll through a long block.
- Applies to agent messages, streaming replies, interactive question text and report cards. Labels are translated (English, French, Spanish). Message data is unchanged.

## 0.29.1 — 2026-11-24

- Refresh the model in Details when a reply arrives. Details read the model only when a conversation was opened, so with Details open it kept "Model not recorded yet" after the agent answered. It now reloads whenever the conversation updates. Recording was already correct in 0.29.0.

## 0.29.0 — 2026-11-24

- Show which model answers in a conversation. Details now lists, under each agent, the model behind the agent's latest reply here, its provider, and whether the agent answers from its main thread or the conversation's own thread. The full model name, thread id and reply time show on hover. A conversation with no reply since 0.29.0 shows "Model not recorded yet".
- Each agent reply records the provider and model Core reported for the call that produced it (`llm.start`, corrected by `llm.done` when a fallback provider answered). If the primary provider fails and the reply arrives before the fallback reports, that reply records no model rather than the wrong one. The record is kept outside message payloads, so public visitors never see it, and outside retry identity, so a retried send stays a duplicate.

## 0.28.0 — 2026-11-23

- Redesign the message composer after Intercom's: a rounded box with the draft on top at full width and a slim tools row beneath it, `+` and the microphone on the left and send/pause on the right. On phones, in the Helper dock and in other chats 480px wide or narrower (the automatic layout), and wherever `composer_layout: compact` is set, `+`, the text and send used to share one row, so a long draft wrapped in a narrow, indented column between the buttons. The text now always gets the full width. Compact keeps the same shape with tighter spacing: one text line that grows as you type.
- Show a quiet grey send button until there is something to send, instead of a faded accent.
- Phones keep the composer closer to the bottom edge (12px, or the home-bar inset plus 8px, instead of 24px).
- `single-line` is unchanged: one fixed row for hosts that ask for it.

## 0.27.8 — 2026-11-23

- Never route a chat led by the Apteva Helper to its main thread. Main routing gives an agent's first operator chat to the agent's own main thread. The Helper has no project and serves every project, so its main thread is shared platform-wide, yet its first chat in a project took main. When a chat is created, Conversations now records whether its lead agent has no project, using the agent list that creation already reads. Those chats always get their own chat thread. Sending a message makes no extra platform call.
- Existing conversations are unchanged. A Helper chat created before this release that has no messages yet can still take main on its first message; start a new chat instead.

## 0.27.7 — 2026-11-23

- Ask agents for a plain numbered list when item titles carry their own sequence numbers: `1. Define the concept`, not `- 01. Define the concept`. 0.27.5 said to use bullets and keep the numbered titles, which put a bullet and a number on every line. The `conversations_send` text field and the skill both say so. Rendering is unchanged from 0.27.6.

## 0.27.6 — 2026-11-23

- Show list items' own numbers as written. A bullet such as `- 01. Define the concept` showed "• 1. Define the concept", and `1. 01. Define the goal` showed "1. 1.": Markdown read the number that starts the item's text as a nested numbered list and renumbered it. That number is now kept as text in messages and Inbox cards, outside code blocks. 0.27.5's advice (use bullets for titles that carry numbers) led agents straight into this.
- Reword the numbered-list advice: `1. 01. ...` shows two numbers.

## 0.27.5 — 2026-11-23

- Tell agents how to write numbered lists. A reply such as `1. 01. Define the goal` rendered as "1. 1." because the leading `01.` starts a second, nested list. The `conversations_send` text field and the skill now say the list supplies the number, and items whose titles already carry numbers go in a bulleted list. Rendering is unchanged.

## 0.27.4 — 2026-11-22

- Let agents without a project, such as the Apteva Helper, reply in conversations they were added to. Their `conversations_send` failed with "authenticated project required", so the reply never arrived. Their calls now run in the conversation's project. They still cannot list or create conversations, and their conversation threads still cannot write to any other conversation.
- Clear Thinking when an agent's turn ends without a reply, for example after a refused send. Core reports when main has finished with a message, and that now settles the response.
- Settle a response on the agent's reply when this install never receives that agent's telemetry, instead of waiting for a receipt that cannot arrive. A project-scoped install does not see telemetry from agents without a project.

## 0.27.3 — 2026-11-21

- Actually clear the working dot and Thinking after main replies in the conversation it answers. The app only listened to Core telemetry for `chat-` threads, so it never saw main receive the message and ignored main's final reply. 0.27.2 fixed how that receipt is matched but could not help without the events.
- Listen to main threads on their own feed. Main's progress, streamed reply text and tool activity now show in the conversation it answers, only while main is answering a message there; main's background work stays out of it.
- Settle main's responses on its durable reply whenever main's feed is unavailable.

## 0.27.2 — 2026-11-21

- Clear the working dot and Thinking after main replies in the conversation it answers. Core delivers those messages as "[console] [chat] ...", so Conversations never saw main receive them and kept the response open after the final reply.
- Put the resume notice after the user's message when a dormant conversation thread restarts, so that first reply also clears Thinking.

## 0.27.1 — 2026-11-21

- Stop a just-deleted conversation's thread in the same pass, ahead of threads still queued from earlier releases. Previously, right after an upgrade, that backlog could delay the stop by one or more five-minute worker passes.

## 0.27.0 — 2026-11-21

- Answer an agent's first operator chat from its main thread. Main receives those messages with a short reply rule and replies there, while keeping its own instructions, tools and global conversation access. There is no new label or setting.
- Keep every other conversation on its own thread. Public, room, agent-created and bridged (Telegram or external participant) conversations never use main. Deleting or archiving the conversation main answers, or bridging it, hands main to the agent's next new chat.
- Show main's activity in that conversation only while it answers a message there; main's background turns no longer appear as Thinking.
- Ask conversation threads to pace themselves: end every turn with an explicit timing decision instead of falling back to a periodic wake.
- Report finished tasks, decisions and agent-wide standing instructions from conversation threads to main.
- Stop a deleted conversation's threads in Core, with retries. Threads leaked by earlier releases are stopped on upgrade.
- After `idle_thread_days` (default 14, 0 disables) without messages, send a conversation thread one idle check. A thread with ongoing timed work keeps itself with the new `conversations_keep_thread` tool; otherwise main gets a short note and the thread is stopped until the next message restarts it with its history.

## 0.26.3 — 2026-11-17

- Align numbered and bulleted list markers with the surrounding message text. The list indent now fits the marker in the current font, so `1.` no longer hangs left of the paragraph edge in monospace themes, and lists with ten or more items get room for two-digit numbers.

## 0.26.2 — 2026-11-16

- Open HTTP, HTTPS and protocol-relative links in chat messages and Markdown report sections in a new tab, keeping the conversation and draft available.
- Apply the same link behavior across dashboard panels, widgets, app-served and packaged React hosts, with noopener/noreferrer protections.
- Preserve relative-route, fragment and email link behavior and existing HTML sanitization.
- Add browser regressions that open a real preview tab and verify the conversation, draft, link protections and report links.
- Pin App SDK v0.115.2, verified as the latest release by commit ancestry.

## 0.26.1 — 2026-11-13

- Scope automatic agent-message deduplication to the thread, accepted user input and full message payload, so providers reusing tool-call IDs such as `call_0` cannot block new replies or Inbox cards.
- Preserve explicit idempotency-key conflict checks and identical retries within the same input context. Existing stored messages need no migration.
- Add deterministic regressions and a real Codex / Sol 6.1 Tier 3 transport fixture that reuses `call_0` and retries every message/card write.

## 0.26.0 — 2026-11-13

- Keep app cards visible in a resizable side panel or draggable, resizable in-page floating view, with compact controls for narrow screens.
- Reuse one mounted renderer for each resource across message references, retaining React state and preserving iframe state on hosts with state-preserving DOM moves.
- Preserve SDK resource locators in messages and tool receipts, scope views by project/conversation/installation, and restore selected references from authorized history.
- Teach agents to present a useful live view once and reuse it through ordinary follow-ups instead of repeatedly attaching cards.
- Document the existing SDK declarations and optional presentation context for producer apps such as Computer.
- Cover live resource reuse and placement changes through Codex / Sol 6.1 and Chromium Tier 3 tests.

## 0.25.5 — 2026-11-13

- Render installed-app message cards using existing SDK declarations across dashboard chats, agent/project widgets and Inbox details.
- Load only declared message components with trusted project/install context, versioned modules, shared caching and refresh on app changes.
- Show readable loading/unavailable states with retry and isolate renderer failures from the conversation.
- Expose optional component adapters for exported React hosts.
- Add real Codex / Sol 6.1 and Chromium coverage with Computer browser previews and Tasks cards.

## 0.25.4 — 2026-11-12

- Keep reports, alerts, approvals, questions and forms in the operator conversation that owns the work.
- Let conversation threads file their own reports, with the same participation and audience checks as other Inbox cards.
- Teach main to preserve the originating conversation through delegation and escalation, search relevant work topics for autonomous work, and create a topic only when none fits.
- Allow sibling apps to target an existing operator conversation through inbox_post.

## 0.25.3 — 2026-11-10

- Answer single-choice questions with one click. Skip stays available, and custom answers send with Enter.
- Guide agents to write concise report summaries and use optional sections for evidence and follow-up.
- Give Inbox headlines more room on narrow screens, remove duplicate alert previews, and show which questions and forms need an answer.
- Guide agents to set alert severity explicitly so failures appear at the right priority.
- Pin App SDK v0.115.1.

## 0.25.2 — 2026-11-06

- Keep typing responsive in long conversations by reusing rendered message rows during draft edits. Live messages and tool updates still render as they arrive.
- Pin App SDK v0.115.0.

## 0.25.1 — 2026-10-31

- Release queued follow-ups strictly one turn at a time. A released message only became an active response once its delivery started, and concurrent delivery workers or activity changes in that gap could release the next queued message too, so the agent received both in one turn. An agent now stays busy until a message already sent to it is delivered, release decisions are serialized, and each agent delivery retries the queue so a turn that finishes before its delivery is confirmed doesn't leave the next message waiting.
- Queue a new message behind an earlier one that is still on its way to the agent, instead of delivering both in the same turn.
- Add a Tier 3 queue-and-steer scenario: three steers sent at different points of a multi-step todo task, two queued follow-ups (one edited) answered afterwards in separate turns, and a removed destructive follow-up that never runs, checked against both the transcript and the Todo data.

## 0.25.0 — 2026-10-31

- Add a Conversations widget for the project Home (`project-conversations`): every agent's conversations in one list, with the lead agent on each row, unread counts, and the chat opened beside the list. Half tiles and narrow containers stack the list and the chat with a back button that returns focus to the row and keeps drafts.
- Start new conversations from the widget with the full page's agent picker, for one agent or a room; the new conversation opens immediately.
- Link the widget to the full Conversations page, default to agent avatars, the compact composer and no shared page context, and give it a fixed height so neighbouring Home widgets don't stretch the chat.
- Export `ProjectConversations` from the React entry and the `project-conversations` UI component for external hosts.
- Reset list styling on the agent widget's conversation list so hosts without Tailwind's preflight don't show bullets.

## 0.24.60 — 2026-10-30

- Keep compact and single-line composers on one row in narrow docks such as the dashboard Helper: an empty draft stays one line tall even while a wrapped "Reconnecting…" placeholder is shown, and the microphone sits after the attachment menu as `[+] [mic] [input] [send]`.
- Keep an active voice session's status and controls on the composer row in single-line layout.
- Accept `single-line` in the `composer_layout` widget-settings type, and mirror the React entry's `composer.layout` into its widget settings.

## 0.24.59 — 2026-10-25

- Keep each queued preview to one line with ellipsis and a compact attachment indicator; show the full text and filenames when editing.
- Pin App SDK v0.108.0, verified as the latest release by commit ancestry.
- Present server-queued follow-ups in a compact tray attached above the composer across the panel, widgets, and exported chat surfaces. Move released/steered messages into the transcript once, preserving FIFO order and server-side storage.
- Edit queued text inline without replacing the new-message draft or its attachments. Keep removal and immediate steering accessible with compact controls, keyboard support, localized labels, and visible retry errors.
- Bound long queues independently of transcript scrolling and use Apteva theme tokens on desktop, narrow panes, and mobile.

## 0.24.58 — 2026-10-24

- Keep Live transcript syllables and trailing punctuation together across interleaved caller captions. Use a bounded caller-first rule for near-simultaneous reply onsets, preserving ongoing answers and the original saved transcript.
- Add the captured French overlap regression, including delayed captions, repeated words, session isolation, and desktop/mobile live updates and reloads across Dashboard, embedded, and package chat surfaces.

## 0.24.57 — 2026-10-24

- Use Info, Warning or Error in alert notification titles through the existing
  generic event template. Preserve recipients, body text, grouping and exact
  message links. Existing notification history is unchanged.

## 0.24.56 — 2026-10-24

- Include `message_id` alongside the conversation ID in message, approval, alert, and report notification links so native clients can target the exact message.
- Preserve notification identities, recipients, grouping, opt-in defaults, and existing publication behavior. Previously saved notifications retain their original conversation-only links.
- Verify all four links through the embedded runtime manifest and assert that published payloads contain both target IDs.

## 0.24.55 — 2026-10-24

- Pin App SDK v0.106.0, verified as the latest release by commit ancestry.

- Align working and unread dots with the right edge of thread rows. Desktop hover or keyboard focus temporarily replaces the dot with the three-dot menu without shifting the title.
- Keep the dot and menu separately visible on narrow and touch screens, preserving title truncation, full-width metadata, existing pulse behavior, and context-menu access.
- Add browser regressions for active/unread positions, desktop hover/focus, touch separation, and supported panel/widget/history indicators.

## 0.24.54 — 2026-10-24

- Restore the full available width for thread metadata and timestamps. Reserve context-menu space only on the title line in the panel, agent conversation browser, and history.
- Let the three-dot action inherit its row background instead of painting a separate tile. Preserve hover, touch, keyboard access, selection, unread/activity indicators, and context-menu behavior.
- Add desktop/mobile layout regressions covering long titles, metadata width, and action overlap.

## 0.24.53 — 2026-10-23

- Render interrupted assistant captions after the caller using preserved provider-timed fragments, including delayed captions. Preserve durable message identities, legacy history, and separate voice sessions. Full ordering support requires the accompanying Core transcript changes.
- Replace separately scheduled audio chunks with continuous AudioWorklet PCM playback and resampling, a 120 ms startup/rebuffer reserve, and smoothing at actual starts and stops. Preserve scoped cancellation, truthful playback receipts, silence handling, microphone streaming, and reconnection isolation.
- Add the captured French interruption regression, sample-level continuity checks at 48 kHz and 44.1 kHz, browser output probing, and real GPT Live Tier 3 transcript-order assertions.
- Validation: 128 Bun tests, 122 browser tests, full Conversations Go race/integration tests, Core realtime race tests, and two real GPT Live browser interruption runs passed.

## 0.24.52 — 2026-10-23

- Advertise playback receipts at connection startup; report partial consumption
  using the audio clock and acknowledge scoped clearing.
- Cancel only the requested playback, reject its late PCM and callbacks, preserve
  unrelated scheduled audio, and isolate callbacks across reconnects.
- Preserve silent PCM without displaying it as speech. Keep microphone audio
  streaming during replies, with debounced speech-onset/end interruption signals.
- Limit bridge renewal and ignore renewals returned after leaving the chat.
- Pin App SDK v0.105.0, verified as the latest release by commit ancestry.
- Add deterministic audio/browser regressions and an opt-in native Tier 3 real
  GPT Live workflow through Conversations, Server, Core and Chromium Web Audio.

## 0.24.51 — 2026-10-23

- Persist live voice transcript checkpoints as revisions of one message per Core transcript identity. Preserve metadata, timestamps and stable message IDs; ignore stale updates and duplicate deliveries, and retain legacy Core compatibility.
- Group adjacent same-speaker voice segments for readable live and reloaded history, without merging across speakers, typed messages, cards, sessions, generations or long gaps. Hide blocked transcripts.
- Add captured GPT Live session replay, concurrent/out-of-order updates, shutdown, recovery, session isolation and browser regressions. Existing historical rows without transcript identities are preserved rather than guessed away.

## 0.24.50 — 2026-10-23

- Use the optional scoped host UI API to hide the floating chat-assistant launcher on compact screens only while a conversation pane is open. Returning to the mobile list, leaving the pane or changing hosts releases the request.
- Support the shared panel, agent widget and exported React chat surfaces without changing manifests or user preferences; older hosts retain their existing behavior.
- Add launcher lifecycle, mobile navigation, desktop resizing, draft preservation and compatibility tests. Pin the latest released App SDK v0.104.0.

## 0.24.49 — 2026-10-22

- Allow one optional suggested next user message attached to a final reply through the existing send tool. Persist it with the reply and offer the same ghost text in the desktop/mobile web composer: Tab or tap prepares an editable draft; sending remains explicit.
- Suppress stale or inappropriate suggestions during active/queued work, existing drafts/attachments, voice, pending approvals/forms and uncertain sends. Add per-client dismissal, host opt-out and live model/browser regression coverage.
- Pin the released App SDK v0.103.0 and keep telemetry subscriptions compatible with its expanded client interface.
- Add a natural coding Tier 3 scenario: independently suggest a shipping bug fix, wait for explicit acceptance, then verify the resulting code and prove the agent's regression tests catch the original defect. Greeting and closure cases must omit suggestions.

## 0.24.48 — 2026-10-14

- Add shared thread context menus to the panel, browser widget and history drawer: rename, mark read, details, archive/restore and confirmed delete. Right-click never selects a thread or clears its unread state.
- Support row ellipsis buttons, mobile layout, keyboard navigation, viewport clamping, host opt-out/custom actions, and existing authorization checks. Preserve composer drafts and active responses.
- Include durable interactive question/form components through the existing send tool, optional late answers, shared inbox/chat rendering, skip/expiry and extensible renderers. Preserve existing approval semantics.
- Pin the released SDK v0.102.0. Add browser regressions alongside the interactive lifecycle, race and real Codex Tier 3 coverage.

## 0.24.47 — 2026-10-08

- Keep model waiting feedback between related tool calls inside the latest tool group without changing completed tool states or durations.
- Clarify that confirmations followed only by Core timed waiting use `phase=final`, so housekeeping before `pace` does not leave Thinking visible; timer wakes start fresh activity.
- Add trace replay and regression coverage for tool-group boundaries and scheduled-wait settling.

## 0.24.46 — 2026-10-08

- Show agent icons by default in the full Conversations panel's header, thread list, and participant views.
- Add `agent_avatars` widget setting / `agentAvatars` React prop (`hidden`, `header`, `threads`, `both`), preserving embedded defaults.
- Reuse the dashboard agent artwork and Apteva theme tokens, with group icons for rooms and a generic fallback on older servers.
- Forward optional authorized agent appearance metadata from Server using app SDK v0.97.0; existing unread, online, and working indicators stay independent.
- Cover appearance modes, selected-agent identity, rooms, fallback, draft preservation, and backend metadata compatibility.

## 0.24.45 — 2026-10-07

- Keep transcript progress and thread-list activity idle during post-reply model work that decides to pace. New input and subsequent timer wakes still start visible activity.
- Keep Thinking visible for genuine proactive turns after earlier final replies or approval cards.
- Update the app SDK dependency to v0.96.0.

## 0.24.44 — 2026-10-05

- Identify attached files from their metadata without asking for an unavailable reader or entering a waiting state.
- Correct composer autosizing after drafts wrap in narrow panes.

## 0.24.43 — 2026-10-05

- Settle the hidden terminal pacing placeholder when its result arrives, so Thinking clears while the agent waits without hiding internal tool lookups or later response work.

## 0.24.42 — 2026-10-04

- Recognize proactive `llm.start` events on both mapped chat threads and subscription, webhook, or timer threads when no inbound acknowledgement exists.

## 0.24.41 — 2026-10-04

- Start visible Thinking progress when a proactive subscription, webhook, timer, or agent-created turn emits `llm.start` before any Conversations acknowledgement exists.
- Settle that progress on terminal pacing events so thread activity cannot remain active after the turn ends.

## 0.24.40 — 2026-10-04

- Settle Thinking after a progress update when the agent enters a long pacing sleep without another model-start event.
- Show one thread indicator when active work and unread state overlap.

## 0.24.36 — 2026-10-04

- Fix startup deadlock with the released App SDK by closing the conversation cursor before recovering queued turns.

## 0.24.35 — 2026-10-04

- Pin the released App SDK support for opaque file-reference passthrough and authorized presentation reads so source installs compile outside the workspace overlay.

## 0.24.34 — 2026-10-04

- Queue follow-up messages by default while an agent is responding, with edit, remove, and steer controls for queued turns.
- Block sends to offline agents and replace unread count badges with the shared unread indicator.
- Reduce progress narration for short multi-step work and add Tier 3 coverage for the quieter flow.

## 0.24.33 — 2026-10-03

- Preserve opaque `blobref://` attachments from native image generation through Conversations messages, history reloads, and authorized image presentation.
- Remove the agent-facing attachment-to-blob bridge now that Core handles shared file references directly; retain scoped reference resolution for delivery and display.
- Add an opt-in real Codex Tier 3 regression covering native image generation, unchanged handoff, telemetry, reload durability, and cross-conversation isolation.

## 0.24.32 — 2026-10-02

- Publish durable, recipient-scoped notification events for messages, approvals, alerts, and reports after committed transcript writes.
- Preserve live delivery paths and add duplicate-safe retry, private-recipient isolation, and streaming-fragment coverage.

## 0.24.31 — 2026-10-02

- Use neutral user-message bubbles in light mode while retaining the existing dark-mode styling.
- Add a real-model two-agent Tier 3 scenario covering normal messages, mentions, shared history, recipient overrides, and retry deduplication; validated with GPT-6.1 Sol.
- Stop pulsing completed tool cards while the model processes their results. Show Thinking during that wait, and pulse the group again only when another tool is preparing or running. Cover refreshes and subsequent turns across shared chat surfaces.
- Remove the agent-facing `read_attachment` tool from the Conversations catalog while retaining its implementation for compatibility and direct access tests. Non-image events keep scoped attachment references and optional Storage file IDs for downstream apps.
- Add the narrow `attachment_to_blob` handoff for non-image files; Core converts its binary result into a temporary `blobref://` handle for tools such as Code ZIP import. Images continue through direct vision content.

## 0.24.30 — 2026-10-01

- Use the native app icon when a legacy integration shares its tool namespace, including Torrent.
- Match tool icons by the actual namespace while preserving exact tool ownership and more specific integration matches.

## 0.24.29 — 2026-10-01

- Keep animated Thinking or a pulsing tool card visible throughout active responses, including after acknowledgements, hidden tool preparation, refreshes, and later turns.
- Settle response indicators when the final reply arrives and clear stale thread activity on idle snapshots.
- Stabilize transcript scrolling during tool updates and preserve the position when reading earlier messages.

## 0.24.28 — 2026-10-01

- Suppress the duplicate generic Thinking row after an acknowledgement while a hidden pacing tool is prepared.
- Keep visible work tools, streamed replies, approval cards, and final responses unchanged.

## 0.24.27 — 2026-10-01

- Normalize spacing around text, lists, tool groups, and cards across panels, widgets, and embedded chats.
- Hide duplicate alert tool activity while retaining the alert card, including after refresh.
- Clear read alert notifications per reader while preserving chat history, newer alerts, pending approvals, and inbox-only items.
- Replace sidebar attention dots with the matching card icon and severity color.

## 0.24.26 — 2026-10-01

- Show resolved approval decisions as compact badges with a check icon and the selected action’s original theme colors.
- Preserve full decision labels, notes, and narrow-pane wrapping across the shared panel and widget.

## 0.24.25 — 2026-10-01

- Keep approval preparation visibly active until its card arrives, including reconnects and either stream event order.
- Show the selected approval option’s full label and note in the chat, including after refresh, with localized fallback for older cards.

## 0.24.24 — 2026-10-01

- Keep approval requests represented by their approval card instead of duplicating the internal approval tool activity.
- Keep completed tool groups visibly active while a multi-step response continues into its next tool call.

## 0.24.23 — 2026-09-30

- Require at least one concise progress message between distinct stages or batches of a multi-step request, including when the first batch completes quickly.
- Add a real Codex scenario and durable transcript assertion covering acknowledgement → work → progress → next work stage → final outcome.

## 0.24.22 — 2026-09-30

- Add concise progress guidance for long multi-step work at meaningful milestones, plan changes, blockers, and input requests, while suppressing per-tool narration.
- Include the progress contract in the Conversations tool description and thread directive.

## 0.24.21 — 2026-09-30

- Require a short visible acknowledgement before every tool-backed user request, including one-call lookups, so the user sees what the agent is about to do before work starts.
- Keep direct no-tool answers and simple image answers immediate.

## 0.24.20 — 2026-09-30

- Keep the named work-tool row pulsing across the preparation-to-execution handoff, including fast calls and reconnects where the durable activity frame arrives separately.
- Preserve tool identity on running progress frames and add regression coverage for the shared panel and widget rendering.

## 0.24.19 — 2026-09-30

- Add one shared embedded-chat contract for host-provided welcome text, draft suggestions, scoped context labels, and idempotent composer insertion without automatic sending.
- Expose explicit acknowledgement statuses for wrong project/agent, closed conversation, archived chat, voice mode, and repeated requests.
- Preserve existing panel/widget defaults, per-conversation drafts and attachments, responsive layout, tool activity, and context-sharing behavior.

## 0.24.18 — 2026-09-30

- Hide Core’s internal parent-thread `send` activity by default across all chat surfaces, including preparation and historical rows after refresh.
- Preserve real work tools such as repository calls, attachment reads, and app-specific send tools.

## 0.24.17 — 2026-09-30

- Keep Thinking and tool preparation active for queued follow-up requests; previous-response housekeeping and final sends cannot settle the new response.
- Register progress before event delivery and transfer ownership when the agent consumes the inbound message, including fast first responses.
- Verify three consecutive replies and reconnects during Thinking, preparation, execution, and continuation, with real animation assertions across panel/widget, external, and package hosts.

## 0.24.16 — 2026-09-30

- Retry transient telemetry subscription failures during startup, including HTTP 401 while a local install is being enabled, so tool activity and voice capture recover automatically.
- Show attachment reads and other Conversations work tools consistently in the shared frontend, native panel, and agent widget.
- Add regression coverage for startup authentication recovery, live activity persistence, cancellation, and frontend tool visibility.

## 0.24.15 — 2026-09-30

- Show Conversations work-tool activity, including attachment reads, while keeping reply sends represented by the resulting chat message.
- Replace the generic attachment glyph with recognizable archive, code, document, and file icons; ZIP attachments now use an archive icon.
- Preserve the read-only attachment flow: binary files remain bounded bytes and are never extracted or executed by Conversations.

## 0.24.14 — 2026-09-30

- Give the shared chat composer a larger bottom inset across the panel and agent widget, matching the Codex-style floating composer spacing.
- Add browser coverage for the composer’s bottom breathing room on dashboard, external, and panel surfaces.

## 0.24.13 — 2026-09-29

- Refresh conversation-list unread counts immediately from durable messages on the existing user-scoped SSE connection, including after reconnect; keep the eight-second refresh as a fallback.
- Show the unread badge on a mobile list after leaving a conversation, even while that conversation remains selected internally. Preserve read-on-view behavior and exported chat.
- Add panel and agent-widget browser regressions for live unread updates.

## 0.24.12 — 2026-09-29

- Drive conversation-list activity through the existing user-scoped SSE stream instead of polling. The indicator starts when a response is acknowledged and clears only after all responding agents settle.
- Send authorized, progress-only snapshots on connect, reconnect, and heartbeat; preserve the existing cursor-free `stream` event and leave exported chat unchanged.
- Add scoped SSE, multi-agent settlement, and packaged panel/widget browser regressions.

## 0.24.11 — 2026-09-29

- Frame the agent Overview chat widget like its neighboring dashboard cards, using the page background and the existing chat header. Keep the Build surface and exported chat embeds unchanged.
- Let header actions wrap in narrow widget widths while messages retain internal scrolling and the composer stays visible. Add widget-scope regression coverage and rebuild the packaged UI assets.
- Pin App SDK v0.89.1, the latest tagged SDK release by commit ancestry.

## 0.24.10 — 2026-09-29

- Offer the existing agent-conversations widget on agent detail pages as well as Build, with half- and full-width options. Agent details default to the focused single-agent chat; Build retains its browser default.
- Keep conversation lists, creation, and history scoped to the host-provided agent. Give the detail widget a usable chat height and stack its browser layout when placed at half width. Add manifest and widget regressions.

## 0.24.9 — 2026-09-29

- Add live voice to existing direct operator conversations through a temporary realtime thread, while keeping typed chat and exported widgets unchanged. Show speech turns in the same conversation and carry recent voice context into the next typed turn.
- Make the mic capability-aware: use a configured project realtime provider independently of the agent's text provider, or dictate into a reviewable text draft when realtime is unavailable. Never auto-send browser speech recognition output.
- Keep voice child lifecycle, authorization, cleanup, and tool activity bounded to its owning conversation. Add Go and browser regressions for lifecycle, capability fallback, transcript handling, and packaging.

## 0.24.8 — 2026-09-29

- Show a small live activity indicator beside responding conversations in the full panel and agent-conversations widget thread lists, including the single-conversation history list. Keep unread and inbox-attention markers distinct, and respect reduced-motion preferences.
- Use one authorized, lightweight activity summary for visible lists rather than a stream per thread. Clear the indicator when a response settles or its activity becomes unavailable; exported chat remains unchanged.
- Add authorization, lifecycle, scoped polling, and packaged mobile panel/widget browser regressions. Pin App SDK v0.89.0.

## 0.24.7 — 2026-09-28

- Give the full Conversations panel a mobile list-to-detail flow instead of stacking the list above an automatically selected chat. Keep the composer in a full-height chat view and restore list position and focus on Back.
- Reduce mobile chrome to Chats and Inbox; move Telegram and archived chats into the overflow menu. Show older-conversation pagination only when a next page exists.
- Leave desktop columns and exported widget navigation unchanged. Add packaged-panel browser coverage for phone widths, navigation, pagination, and widget isolation.

## 0.24.6 — 2026-09-25

- Create conversations from the in-app panel as operator conversations without an audience selector. Public conversations for external sites remain unchanged.
- Add a packaged-panel browser regression test for the modal and its create request.

## 0.24.5 — 2026-09-23

- Keep streamed replies, preparing tools, executing tools, and saved messages in one chronological transcript. Retain completed streamed text until its durable replacement arrives.
- Publish immediate response progress, restore Thinking after acknowledgements and completed calls, and reject delayed preparation events that would revive completed tools.
- Restore current response progress and partial text on SSE reconnect; periodic snapshots repair dropped ephemeral frames without changing durable message cursors.
- Rebuild native widget/panel bundles as well as app-served assets. Browser tests now load the actual packaged dashboard widget, and release tests reject stale compiled panel sources.
- Add a sanitized replay of the reported Processes telemetry plus delayed-delivery, reconnect, streaming-order, and shipped-surface lifecycle regressions. Pin App SDK v0.88.0.

## 0.24.4 — 2026-09-23

- Keep grouped tool activity compact when many apps or integrations are involved: show the source for the visible latest action in front, with at most one earlier source peeking behind it. Expanding the group still shows every call and its own source icon.

## 0.24.3 — 2026-09-23

- Make the shared dashboard and exported-chat tool display configurable. It now defaults to the source icon and activity text; completion checks and durations are optional.
- Hand progress back to Thinking when a tool finishes, retain subsecond message ordering alongside tool telemetry, and require a completed acknowledgement call before multi-step work tools.
- Carry an attached image's visual finding into the first acknowledgement before its transient model input expires. Clarify operator-conversation creation for escalations and approvals.
- Add a live acknowledgement-before-tool assertion; all 14 Tier 3 scenarios passed with GPT-6 Luna.

## 0.24.2 — 2026-09-22

- Restore the host-personalized conversation workspace rail in the full Conversations project page, with app-owned Details plus eligible `dashboard.thread_sidebar` widgets.
- Keep exported conversation and Inbox widgets unchanged, and retain the standalone Details column when the host does not provide a workspace rail.

## 0.24.1 — 2026-09-21

- Remove the redundant manual Refresh control from the Inbox widget; app events and the 15-second fallback poll continue to keep it current.

## 0.24.0 — 2026-09-21

- Expose the existing Inbox dashboard widget in global dashboards as well as project dashboards.
- Add a global Inbox view that combines only the authenticated user's visible projects, with project filtering and labels while preserving the existing priority and cursor ordering.
- Keep Inbox mutations project-scoped by carrying each item's project through approval, dismissal, and seen actions.
- Upgrade to App SDK v0.85.0, the combined release containing both the existing chat/v1 surface contract and global dashboard scopes.

## 0.23.26 — 2026-09-20

- Accept the platform's signed first-party bearer principal on Conversations HTTP routes instead of misclassifying it as an incomplete delegated application user.
- Keep delegated application-user isolation fail-closed whenever an issuer marker is present but the external identity or scope is incomplete.

## 0.23.25 — 2026-09-20

- Bind the generic `chat/v1` surface to the established `/chats?page=1` and `/messages?page=1` page contracts while preserving every legacy dashboard response shape.
- Close the SSE replay/live handoff gap by deduplicating buffered changes with the durable `message_changes` cursor; ephemeral `stream` frames remain cursor-free.
- Pin source installs to the immutable `conversations/v0.23.25` release ref and add release-artifact, packaged-surface, page-contract, mark-seen, and reconnect coverage.

## 0.23.24 — 2026-09-20

- Publish Conversations as an optional `mobile.project_app` using the generic `chat/v1` native surface contract, with app-owned conversation, agent, history, send, create, seen, and subscription routes.
- Add opt-in summary and cursor response modes without changing the existing dashboard response shapes.
- Make durable SSE messages named and revision-cursored so reconnects replay both new messages and updates, while ephemeral stream activity remains uncursored.
- Let a missing or zero seen cursor atomically mark through the latest visible durable message.

## 0.23.23 — 2026-09-19

- Preserve the mounted app service identity for background and cookieless HTTP work while deriving request-scoped platform credentials for signed-in browser requests.
- Restore browser-session propagation through the generic artifact pipeline, with regression coverage proving the mounted context is never replaced.

## 0.23.22 — 2026-09-18

- Rebuild the manifest-loaded `AgentConversationsWidget.mjs` so the generic `show_page_context: false` presentation setting shipped in 0.23.21 is honored at runtime.
- Add a release regression check that rejects a stale compiled widget bundle missing the declared page-context display setting.

## 0.23.21 — 2026-09-18

- Add the generic `show_page_context` agent-conversation widget setting. It defaults to `true`; setting it to `false` hides the complete context row without changing the page-context snapshot attached to sent messages.
- Keep transport and presentation independent, with regression coverage proving hidden context is still posted intact. Conversations contains no onboarding- or app-specific display behavior.

## 0.23.20 — 2026-09-18

- Carry the operator's explicitly shared dashboard page identifiers through the Conversations widget, durable message metadata, retries, and agent events. Context is project-bound, allowlisted, size-limited, and clearly marked as untrusted descriptive data rather than instructions or authorization.
- Show the context snapshot beside the composer with a per-page remove action, keep it out of public conversations, and expose the same typed option to app-served and npm React chat surfaces.
- Add browser-level transport coverage and an HTTP-to-durable-store-to-agent-event regression test so future frontend or delivery changes cannot silently drop context again.

## 0.23.19 — 2026-09-15

- Hide the exact internal `search_tools` lookup from preparation, live tool activity, summary counts, and historical activity. Other search/query tools remain visible.
- Apply the same filtering to the Conversations backend, dashboard panels, embedded chat, and npm React exports.

## 0.23.18 — 2026-09-14

- Keep consecutive tool calls on one updating, expandable summary line, including across long pauses and after reload. Only intervening messages split tool groups; remove the former 30-second cutoff.
- Preserve live pulsing, individual call details, and actual execution durations in dashboard, embedded, and npm-exported chat.

## 0.23.17 — 2026-09-14

- Show an immediate response indicator on Send, including while the request is in flight; clear it on failure or replace it with confirmed live progress.
- Keep the current response’s latest tool group pulsing across results, model preparation, parallel calls and intermediate replies. Keep tool labels and completed execution durations; avoid a separate Preparing response row once tools own progress.
- Apply the same behavior to dashboard, embedded and npm-exported chat.

## 0.23.16 — 2026-09-14

- Restore live tool preparation and pulsing continuation from the dashboard chat lifecycle. Model and tool events drive progress; background work after final replies and approval waiting stays silent.
- Paint rapid tool starts before results while preserving actual execution durations. Report Core success=false results as failures.
- Clear pending thinking when an approval card is delivered and start a fresh indicator when the approval verdict reaches the requesting agent. Preserve room isolation and old-card replay handling.
- Share the lifecycle and animations across dashboard panels, app-loaded embeds and npm React exports.

## 0.23.15 — 2026-09-14

- Define approval action IDs, labels, styles and bounds in the tool schema, including the app-only inbox tool, so agents do not have to discover required fields through failed calls.
- Ask for approval directly through the card without a redundant announcement message.
- Give unstyled first choices and destructive choices an accent outline; keep alternatives neutral, including the default Deny action. Applies to existing cards and dashboard/exported chat.

## 0.23.14 — 2026-09-14

- Hide all Conversations tools from live and historical activity, including approval requests already represented by cards.
- Use the host accent for primary approval actions and neutral outlined secondary actions and status badges in both dashboard and exported chat.
- Include the shared progress and execution-duration fixes from 0.23.13 in the public npm package.

## 0.23.12 — 2026-09-13

- Hide integration logos in narrow chat tool rows (up to 640px) so tool text and status controls remain aligned and readable. Roomy chats keep the existing icons.

## 0.23.11 — 2026-09-13

- Preserve both direct Core vision content and the bound Storage `file_id` for image attachments. Downstream tools can now attach the original image after the agent receives it.
- Add Tier 2 coverage and an opt-in Tier 3 real-Codex scenario that creates a Tickets record and verifies `storage_file_id` on its attachment.


## 0.23.10 — 2026-09-13

- Authorize delegated `GET /tool-visuals` requests with the explicit `tool_visuals.read` action. Requests without that permission remain forbidden, and the frontend keeps static icon fallbacks.
- Add registered-route regression coverage for authorized and unauthorized delegated users.


## 0.23.9 — 2026-09-13

- Map `GET /tool-visuals` to the explicit delegated action `tool_visuals.read`, allowing authorized external users to load integration-logo metadata instead of receiving an unconditional 403.
- Document the exact issuer-policy permission and cover the registered route with allowed/denied tests. Metadata access does not grant conversation access; fallback icons remain available without the permission.

## 0.23.8 — 2026-09-13

- Add typed `composer={{ layout: "single-line" }}` with a fixed one-row textarea and inline attachment/send controls, preserving multiline drafts, images, screenshots, French labels and pause behavior. Existing compact/auto modes still grow with text.
- Publish the shared UI as `@apteva/conversations@0.23.8` for hosts that pin their frontend dependency. Dashboard and app-served bundles use the same implementation.
- Namespace the shared base CSS layer so npm consumers using Tailwind v3 (including Flexylead) can import the compiled stylesheet.
- Reject unsupported runtime layout values instead of silently rendering a default.

## 0.23.7 — 2026-09-13

- Rebuild the release against the pinned SDK-compatible HTTP context path so source installs can upgrade without requiring an unpublished SDK method.

## 0.23.6 — 2026-09-13

- Restore integration logos in ChatToolActivity by loading project runtime catalog metadata, with static icons and generic glyphs as fallbacks.

## 0.23.5 — 2026-09-13

- Restore project integration logos in tool activity by loading runtime catalog integration metadata, with static app icons and generic glyphs as fallbacks.

## 0.23.4 — 2026-09-13

- Make archiving and unarchiving responsive: mutation responses avoid an unnecessary platform agent lookup, and the UI removes the row immediately without waiting for a full unread/inbox refresh.
- Preserve configurable empty transcript text for agent conversation widgets and complete its shared chat prop wiring.

## 0.23.3 — 2026-09-12

- Render compact user image thumbnails above the message text, aligned right, in the shared dashboard and exported chat. Keep enlargement, image metadata and original downloads in the image dialog.
- Deliver current images directly as visual content without file-retrieval instructions. Update Conversations-owned thread instructions and skill to answer simple image questions directly, without a preliminary acknowledgement or attachment-reading call. Core and SDK behavior are unchanged.
- Verify original image payload preservation, mixed image/file routing, thumbnail geometry, history and downloads in both chat hosts.

## 0.23.2 — 2026-09-12

- Add composer.layout (auto, compact, expanded). Auto is the default and uses a single row in chat containers up to 480px wide. Explicit modes override responsive selection.
- Expose Composer layout in dashboard widget settings. Preserve drafts, attachment previews, multiline growth and send/pause behavior in both modes and both hosts.
- Verify responsive container sizing, explicit overrides and draft preservation alongside the full 29-browser-test and 31-unit/UI-test suites.

## 0.23.1 — 2026-09-12

- Refine the shared composer with explicit inherited sans-serif typography, aligned text/icon insets, matching 36px controls and 20px SVG icons, a lighter send arrow and a subtle focus border. Preserve enlarged touch hit areas and keyboard focus indicators.
- Align attachment-menu labels and icons; verified identical geometry in dashboard and exported chat, with desktop/mobile visual review and all 25 browser checks passing.

## 0.23.0 — 2026-09-12

- Add the shared configurable + composer menu for files/photos, pasted or dropped attachments, and one-frame screenshots, with native capture and custom action callbacks.
- Persist image previews and downloadable file cards in messages and history. Support attachment-only sends, upload retry, per-chat drafts, and sending while an agent is active.
- Store bounded originals with conversation-scoped access. Send real image content through Core thread events; expose bounded text and binary reads through conversations_read_attachment.
- Optionally copy sent files privately into a bound Storage app and return file IDs, without exposing Storage permissions to chat visitors.

## 0.22.2 — 2026-09-12

- Restore the original `ChatToolActivity` renderer and scoped styles in the shared dashboard/exported UI: stacked app icons, animated activity text, expandable groups, parallel calls, failure indicators and elapsed times.
- Restore live tool activity in the common dashboard/exported transcript, with persisted status, elapsed time, conversation-scoped access, revision-safe refresh recovery and no raw arguments/results in the client feed. Previously recorded platform telemetry is not imported.
- Integrate advisory pause into the composer action while a reply or tool is active; typing switches back to send without waiting for the agent.

## 0.22.1 — 2026-09-10

- Ship the same scoped stylesheet in native dashboard panels and exported chat, including Tailwind utilities, Markdown and mobile layout. Preserve host theme tokens and contain defaults within Conversations.
- Restore uncluttered transcripts: hide successful delivery diagnostics and single-agent identity labels, resolve display names for multiple speakers, and restore message spacing. Keep localized failed/unconfirmed delivery notices and retry actions.
- Pin the app’s Go SDK to v0.77.0, the latest tagged ancestor of SDK HEAD.
- Verify real Markdown replies, long links, mobile widths, live streams, retries, theme inheritance and computed layout parity in both hosts.

## 0.22.0 — 2026-09-10

- Add per-surface `locale`, `timeZone`, and `messages` props to the app-served React components and dashboard wrappers, with English, French, and Spanish UI dictionaries and locale-aware dates, relative times, numbers, and plurals.
- Keep UI language changes independent of chat state: drafts, pending sends, transcripts, and subscriptions survive locale updates. Preserve authored content and API values.
- Document host wording overrides and add rendered and browser regression coverage for localization through the existing Web SDK loader.

## 0.21.2 — 2026-09-07

- Add an app-only identity resolver for explicitly trusted backend installations. Resolve external principals from active conversation bindings; deny unbound workers, wrong scopes and archived conversations. Resolution is disabled until trusted installation IDs are configured.

- Clarify in the skill, chat instructions and MCP descriptions that Conversations owns chat configuration, including when the caller is main. Keep visitor-dependent work in its authenticated conversation and explain recovery from an unbound-worker identity failure.
- Add a real-Codex ownership regression that audits mutation attempts and configuration, challenges main and the visitor thread, and verifies history and restart/resume. This is a behavioral mitigation, not a new Core enforcement mechanism.

## 0.21.1 — 2026-09-07

- Allow main to acknowledge its own resolved approval with an explicit approval message ID; keep ordinary replies restricted to conversation threads.
- Deliver precise receipt instructions with the approval verdict, including denials; deduplicate repeated acknowledgments.
- Add regression coverage for pending decisions, wrong agents/conversations/threads and ordinary-reply restrictions.

## 0.21.0 — 2026-09-07

- Ship the headless client, shared React UI and scoped styles with the app under `/ui/frontend.json`.
- External hosts use Web SDK 0.7.0 `apps.load` without installing or publishing a Conversations npm package.
- Keep dashboard wrappers, component names and authorization behavior. Frontend assets use content hashes and the host’s React instance.
- Build app frontend assets automatically with the normal panel build; mark the app frontend package private.


## 0.20.0 — 2026-09-07

- Export `@apteva/conversations`: a headless Web SDK extension, the existing chat,
  inbox, approval/report/alert cards, TypeScript declarations and scoped styles.
- Share one UI implementation between dashboard wrappers and external hosts.
  Keep manifest component names, dashboard theme, saved drafts, send retries,
  delivery status and durable history replay. The SDK is bundled into dashboard assets.
- Use published Web SDK 0.6.0 for scoped HTTP and authenticated SSE.
- Isolate application-user subjects from their issuer’s installer account and
  each other; enforce explicit actions, permitted agents and public audience.
  Apply the same boundary to paginated lists, inbox totals and unread summaries.
- Add migration 010 for external identities without changing existing owners.
  Reset shared UI state when its client identity changes.


Validation: 134 Go tests with race detection, 25 frontend tests, strict TypeScript,
four browser fixture tests, production dashboard bundles and a separate packed-package
consumer build passed. Go vet and Go/npm vulnerability checks passed. Real browser
checks verified dashboard cookie auth, external bearer/CORS/SSE, real Codex replies,
responsive layout and subject isolation. All 12 Tier 3 workflows passed with real Codex on the final candidate.
See `VALIDATION-0.20.0.md` for live-suite evidence.

## 0.19.0 — 2026-09-05

Requires Apteva **0.40.6 or later**. Source builds require Go **1.26.8 or later**;
the app is pinned to app-sdk v0.73.0. This release adds database migration 009.

- Enforce conversation privacy and ownership across HTTP, MCP, SSE, card actions,
  keyed creation, roster changes, and archived or deleted conversations.
- Deliver agent work through a durable leased queue with bounded workers, stable
  retry identities, lifecycle receipts, and visible delivery status. Approval
  verdicts return to their original thread, including main.
- Recover Telegram inbound work after interruptions; preserve conversation
  ownership and participants during rotation. Split long replies into durable
  parts, preserve report sections, and expose uncertain delivery outcomes for
  explicit operator handling. Validate bot-addressed commands before processing.
- Page conversation lists, inbox results, and history. Recover edits through a
  durable change cursor and reject stale history using per-message revisions.
- Preserve drafts and pending send identities across navigation. Keep unread
  markers tied to visible history and isolate streaming feedback by agent/run.
- Render attachments and structured report content, preserve unsaved Telegram
  settings, and surface action errors.
- Bound message size, streaming state, caches, and delivery retention. Incremental
  stream parsing reduces cumulative allocation by 99.1% in the 64 KiB benchmark.
- Update the Go minimum and x/sys dependency to clear the audited advisories.

Validation of the release implementation: 129 Go unit/integration tests with race
detection, 21 UI tests, strict TypeScript, three production UI bundles, Go vet,
and dependency vulnerability scans passed. All 12 end-to-end workflows passed
with real Codex, including conversation isolation, reports, alerts, approvals,
public visitor behavior, and two-agent delivery. Main-thread approval also passed
three consecutive additional runs. Telegram transport was tested against a
controlled gateway; UI behavior was tested with React in a DOM harness.
