# Conversations tests

Use `apteva test` for every tier, from this directory, with the pinned Go SDK:

```sh
GOWORK=off apteva test --tier 1 .
GOWORK=off apteva test --tier 2 .
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-5.6-terra scenarios
# Or all checks, retaining failures from earlier tiers:
GOWORK=off apteva test --tier all --provider openai-codex --model gpt-5.6-terra scenarios
```

This suite requires the CLI runner with `setup.driver`, `setup.apps`,
`setup.agents`, and `apteva.test.yaml` support. Build the CLI checkout with
`go build -o /tmp/apteva-test .` in `apteva/` when testing unreleased runner changes.
Use the resulting executable in place of `apteva` above. Core is reused unchanged.

Tier 1 runs all Go unit tests, the dashboard/exported-chat Bun tests, and the
public composer type checks. Tier 2 runs the Go sidecar integration tests and
Playwright dashboard/exported-chat browser tests. `apteva.test.yaml` adds frontend
commands to the runner's standard Go commands; it does not replace them.

Tier 3 provisions a disposable server/project, the actual local app binaries,
install configuration/bindings, and real model-backed agents. Set
`OPENAI_CODEX_ACCESS_TOKEN` and `OPENAI_CODEX_ACCOUNT_ID` in your environment or
private `~/.apteva/test.env`. Never commit credentials. `APTEVA_SERVER_BIN` and
`APTEVA_CORE_BIN` can select existing server/Core binaries. The runner also offers
`--server` for using server-side provider configuration; use a dedicated test
server, since these workflows deliberately create data and restart test agents.

Every YAML file is one native runner scenario. The scenario driver is an argv
command run without a shell. Its Go client workflow receives the runner-owned
agent IDs, install IDs, project, URL and owner credential through environment
variables. It fails when runner resources are missing and does not reuse a personal
agent or provision another platform. The runner independently collects real
telemetry, checks YAML trajectory assertions/token budgets and removes resources.
Drivers preserve HTTP/SSE, retry, approval-card, public-audience, negative-space,
and restart/profile assertions that need several interactions during an LLM run.
Do not invoke the `scenario` Go build tag directly.

## Live coverage

| Scenario | Preserved outcomes |
| --- | --- |
| Chat round trip | Real reply persisted in the originating chat |
| Single conversation | Latest lead-agent lookup and focused chat reply |
| Soft break | Live SSE call ID; targeted break consumed; retry is idempotent |
| Image → Storage → Tickets | Acknowledgement before visible tool work; actual colour recognition; unique ticket; exact Storage ID in message, tool args and ticket; retry deduplication |
| Two conversations | Neither chat receives the other's response |
| Alert | Agent-created, titled operator conversation and warning/error inbox card |
| Approval round trip | Advertised action accepted, card updated in place, verdict acknowledged |
| Report | Same report present in inbox and transcript |
| Persistent resource views | Real Codex presents a resource once, performs two normal follow-ups without duplicate cards, explicitly refreshes the same reference, then opens a second resource; Chromium verifies side/floating/inline placements, retained component and iframe state, live updates, switching and history restoration |
| Installed app cards | Real Codex opens a local Computer browser session and attaches Computer/Tasks cards; Chromium loads actual installed modules, a durable final screenshot and working task details across widget/panel/project/Inbox surfaces; review preserves the operator draft |
| Work conversation Inbox | Local reports, alerts and approvals stay in the originating conversation; a scheduled follow-up reuses it without creating a generic topic |
| Public escalation | Visitor reply with no inbox leakage; escalation in operator chat |
| Public self-service | Reply without escalating during a settle period |
| Public refusal | Reply without escalating during a settle period |
| Room fan-out | Two runner-owned agents reply; duplicate submission reuses one row |
| Room messages and mentions | Normal messages/follow-ups reach the lead; named mentions reach only that participant; shared history is readable; `@all` reaches both; explicit recipients override mentions; retry deduplicates; each round checks final replies and a 12-second quiet window |
| Form answer shows live work | Main asks with a blocking form; after the answer, Thinking shows in the conversation and every `todo_todos_complete` call is a tool row before the final reply; Thinking ends with it |
| Thinking ends after reply and pace | The agent replies, then paces with `clear_wake`; Thinking ends with the reply and stays off for 15 s, and the next message is released, not queued, and answered |
| Short multi-step cadence | Related list and todo lookups complete with one acknowledgement and one final reply, without an unnecessary progress message |
| Long multi-step progress | A meaningful stage boundary produces one intermediate progress update before the final outcome |
| Queue and steer | Three steers sent at different points land in the running todo task; two queued follow-ups (one edited) run afterwards, each in its own turn; a removed destructive one never runs; Todo data and transcript both show all work done |
| Bound approval | Approval/verdict remain in the originating chat |
| Ownership | Unchanged main/chat profiles, no mutation/delegation attempts, history recall and restart/resume |

The image scenario declares Storage copying in `setup.app.config`, enables the
optional Storage binding, and installs Tickets via `setup.apps` with the same
Storage binding. Tickets is a test peer, not a production dependency of
Conversations. `spawnable: true` makes the selected apps available to the
app-owned conversation thread. No preconfigured live install or Tickets agent
is required.

Scenario 25 also provisions real Computer and Tasks peers. It requires local
Chrome for Computer, Playwright Chromium, and the workspace's sibling `ui-kit`
source to build a dashboard-like test host with one shared React runtime. The
model opens/closes a real local webpage, attaches both app cards, and then adds
an optional Inbox question alongside those same cards. Browser assertions use
the real runner APIs and installed UI modules without mocking producer data.
The deterministic browser suite separately covers unavailable apps/imports,
retry, project scope in a global Inbox, and narrow layouts.

The image test uses the uploaded-attachment path used by the composer. It does
not prove that legacy `data_url`-only submissions without an attachment ID are
mirrored. The config toggle remains required; a Storage binding alone does not
enable copying.

To run one workflow, pass its YAML file instead of the directory. For repeated
LLM evaluations, set `runs` and `required_pass_rate` in that scenario. Results
include driver output, actual tool calls, iterations and token totals with
`--json`; failures also retain artifacts under `--artifacts-dir`. A zero cost
reported for Codex subscription usage does not mean zero model usage.

For a two-agent conversation with ordinary follow-ups and typed mentions:

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol --json scenarios/16-room-messages-and-mentions.yaml
```

This checks durable messages, routing, thread ownership, shared history, and
duplicate replies. It does not exercise browser rendering, the mention picker,
or simultaneous approvals. The runner verifies the requested model against
actual `llm.start` telemetry.

For the native image handoff regression, run the dedicated opt-in scenario with
the real Codex provider and GPT-6.1 Sol:

```sh
APTEVA_RUN_CONVERSATIONS_NATIVE_IMAGE_TIER3=1 \
  GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/17-native-image-to-conversation.yaml
```

The scenario enables Core's provider-level `image_generation` capability for
that disposable agent. It verifies that the generated `blobref://` survives the
native image turn, is passed unchanged to `conversations_send`, remains in a
reloaded transcript, resolves through the authenticated conversation attachment
route as the original PNG, and cannot be fetched through another conversation.
It is intentionally separate because it consumes hosted image-generation
usage; ordinary Tier 3 runs do not enable the capability.

Latest local validation (2026-10-02): one run passed all eight rounds with two
`openai-codex` / `gpt-6.1-sol` agents. Runtime telemetry confirmed the provider
and model. Elapsed time was 179.3 seconds, with 23 iterations and 232,143 total
tokens. Each intended recipient produced exactly one final reply per round;
no untargeted replies or duplicate finals appeared during the 12-second settle
windows. The test used a disposable local server/project and the current local
Conversations source. The Go unit suite also passed. This is one successful
live run, not a measured reliability rate across repeated runs.

Old `go test -tags live` commands and manual install/agent bootstrap scripts are
superseded. Historical release validation documents retain their original run
results; they are not instructions for the current suite.

## Interactive components

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/19-interactive-components.yaml
```

This provisions an isolated agent and tests an optional question followed by a
final reply without answering it, idle activity while the question stays in the
inbox, a later answer delivered to the same thread, saved results, identical
retries and conflicting answers, and a blocking form that resumes after input.
No additional agent tool is introduced. Unit/race checks additionally cover
concurrent card updates, missing/invalid values, skip retries, private access,
archiving, durable delivery recovery, expiry, and inbox opt-out. Browser checks
cover widget/panel submission, mobile widths, reload, inbox clearing and skipping
required fields, alongside the existing approval/report/alert tests.

Local validation on 2026-10-09: the real `openai-codex` / `gpt-6.1-sol` scenario
passed in 113.2 seconds, with 10 iterations and 146,625 total tokens. Actual
telemetry verified the provider/model. This is one successful run, not a
reliability-rate estimate.

The first attempt found a Server subscription startup limitation: app telemetry
caches eligible agents and refreshes them every 60 seconds
(`server/apps_telemetry.go`, `telemetryOwnedAgentsRefresh`). The runner installs
Conversations before creating the agent, so its first turn can occur before the
app receives that agent's `event.received` and completion events. The question
and final reply were saved, but activity remained active. This scenario waits
65 seconds for freshly provisioned agents to enter the subscription before its
first user turn. The Server limitation remains; no Server code was changed.

Final checks for this implementation passed: the full pinned-SDK Go suite,
interactive-component race checks, sidecar integration tests, 106 Bun UI/client
tests, 90 Playwright browser tests, frontend builds, and public consumer type
checks. Older browser assertions were aligned with the already-shipped tool-row
Thinking animation and unread dots. The spacing regression checks caught an
empty component wrapper; it now renders nothing for ordinary messages without
components.

## Thread-menu release validation — 2026-10-14

The panel, browser widget and history drawer share the same thread action menu.
Browser regressions cover right-click without navigation/read acknowledgement,
rename/read targeting, draft preservation, keyboard focus, mobile/viewport-edge
positioning, archive/restore, confirmed deletion, permission errors, details,
host opt-out and custom actions. Existing activity, composer, inbox, attachment,
unread and transcript-spacing regressions remain covered.

Release v0.24.48 passed 106 Bun tests, all 95 Playwright tests, consumer type
checks, the full pinned-SDK Go suite, and the full Go race/integration suite.
A fresh isolated Tier 3 `openai-codex` / `gpt-6.1-sol` run passed in 130.5 seconds
with 10 iterations and 147,422 total tokens, verifying optional and blocking
widget flows again using the current local Server/Core binaries. No live user
agents or production instances were restarted for this validation.

## Suggested next message

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/20-suggested-reply.yaml
```

This lifecycle scenario uses a disposable project/agent. It explicitly supplies
the suggested wording, so it proves the handoff rather than suggestion quality.
It checks durable reload,
no inbox attention, idle activity and a quiet window without automatic work,
then explicitly sends the suggested text as a user turn. It verifies exactly
one continuation despite a send retry and no suggestion after closing the chat.
The paired browser suite tests actual Tab and touch acceptance, proves no send
or action endpoint is called until the user submits, and covers dismissal,
reload, existing drafts/attachments, pending input/sends, active/queued turns,
conversation switching, host opt-out, IME and ordinary keyboard navigation.
These are complementary checks: the Tier 3 driver exercises the model/server
handoff; the browser tests exercise the composer UI.

Local model run on 2026-10-22 passed with actual `openai-codex` / `gpt-6.1-sol`
telemetry: 95.2 seconds, 7 iterations and 102,433 total tokens. The run contains
three conversation sends: the final with one suggestion, the response after
actual user submission, and the closing response. No additional suggestion tool
or generation request was introduced. The runner creates its own isolated
Server/Core processes and leaves existing local and production agents alone.

For a natural coding task that tests relevance and usefulness, run:

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/21-coding-suggested-reply.yaml
```

This installs Code as a disposable test peer and seeds a small shipping-cost
repository with an exact-threshold bug and passing tests that miss that boundary.
The user asks only for an investigation. The scenario directive asks in plain
words for "the most useful next step as a suggested reply" after an
investigation, as an operator who wants suggestions would; nothing names the
component, its schema or the wording. The shipped Conversations guidance must
teach how the suggestion is attached. (Until 2026-11-27 the directive did not ask
at all. Suggestions are optional in the guidance, so the scenario passed only when
the model volunteered one, about half the time or less; that unprompted variant
is now scenario 32, opt-in with
`APTEVA_RUN_CONVERSATIONS_UNPROMPTED_SUGGESTION_TIER3=1`, for measuring the rate
after a guidance change.) The driver
checks an explicit relevance rubric (a shipping/boundary fix plus regression
coverage), durable history, unchanged source and a quiet window before acceptance.
It then sends the exact generated text without extra instructions. Independent
Bun checks verify the retrieved implementation at and around the threshold, and
reintroducing the original bug must fail the agent's own regression suite. Natural
greeting and closing messages must have no suggestions. This is evidence for one
concrete coding workflow, not a general suggestion-quality benchmark.

Local command execution is enabled only on this runner-owned Code install. No
live app settings, installed local agents, or production resources are changed.

The natural coding run on 2026-10-22 passed with verified `openai-codex` /
`gpt-6.1-sol`: 160.1 seconds including startup/readiness, 19 iterations and
322,479 total tokens. The model independently generated:

> Fix the threshold comparison, add the exact-$50 regression test, and run the tests.

It read all three fixture files and confirmed the existing two tests passed
while `shippingCents(5000)` returned `499`. No source changed before the exact
suggestion was sent as a user message. After acceptance, it added the boundary
test, observed its failure, changed `>` to `>=`, and ran three passing tests.
The driver's independent policy suite passed four tests with 11 assertions;
restoring the original implementation produced two passing tests and the
expected failing boundary regression. Greeting and closure had no suggestions.
No production or installed-app update was performed.

Final local validation passed: 108 Bun UI/client tests, all 107 Playwright
browser tests (including actual touch/mobile viewport checks), consumer type
checks, packaged UI builds, and `GOWORK=off go test -race -tags=integration ./...`.
The dedicated real-model Tier 3 run above passed separately. This validation
covers the v0.24.49 release; publishing does not update installed apps automatically.

## Queue and steer while the agent works

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/23-queue-and-steer.yaml
```

The agent works through a five-item Launch prep todo list. Every user message
goes out with the composer's default queue behavior while that task runs:

- Three steers, sent at different points: right after the task starts (add and
  complete "Notify the team"), after the first todo is completed (leave the last
  still-open seeded todo for the user, reopening it if needed), and after the
  second (list completed work and include `STEERS_APPLIED` in the summary). The
  later steers wait for completions in the Todo data, not for chat messages.
- Two queued follow-ups: create a Retro list (edited while queued; the original
  text asked for a Draft list), then report the open-todo count.
- One queued "Delete the Launch prep list" message, removed before it is sent.

The task must end with one final summary carrying the last steer's token. The
follow-ups must then be answered once each, in order and in separate turns,
followed by a 15-second quiet window. The Todo data must show every seeded
todo and "Notify the team" completed except the one handed back, Retro holding
one open "Collect feedback" todo, no Draft list, Launch prep intact, and an
open count matching the data. The trajectory also forbids list and todo
deletion. The browser suite separately covers the queued tray's controls.

Local runs on 2026-10-31 with actual `openai-codex` / `gpt-6.1-sol` telemetry:

- Before the queue fix: one run passed (91.7 seconds, 23 iterations, 421,767
  tokens). In another, all work was done but both follow-ups reached the agent
  together right after the task and were answered in one message. A released
  row only became an active response once its delivery started, so a second
  release pass in that gap (another delivery worker or activity change)
  released the next row too.
- After the fix (releases serialized; a sent but undelivered message keeps its
  agent busy; each agent delivery retries the queue): two consecutive runs
  passed with each follow-up answered in its own turn, in 123.5 seconds /
  29 iterations / 543,056 tokens and 84.6 seconds / 23 iterations / 422,040
  tokens. `TestQueueReleasesOneRowWhileDeliveryIsPending` and its neighbours in
  `queue_test.go` reproduce the gap deterministically.

## Scoped host launcher visibility (v0.24.50)

The new `host-ui.browser.ts` contract tests exercise the built dashboard panel,
agent widget, app-served exports and package exports. They verify mobile list →
conversation → Back cleanup, preserved drafts on reopening, restored launcher
on inbox navigation, desktop visibility, responsive resizing without remounting
the chat, and capability-disabled compatibility. Existing tests omit `host`,
covering older hosts. React lifecycle checks also replace/remove the host while
retaining the conversation and draft, and verify cleanup on unmount.

These fixtures exercise Conversations' request timing and prop forwarding. The
platform's separate `hostUI` and `ChatAssistantDock` tests cover foreground,
navigation/project/installation isolation and preservation of open Helper chats,
drafts and voice sessions. No real-model test is needed for this presentation
change: it adds no agent input, tools, events or message behavior.

The host API is currently local/unreleased in the platform. Conversations
feature-detects it and uses only a structural type; the manifest has no new
metadata requiring the unreleased SDK. Older platform releases keep showing
their launcher until the corresponding Dashboard support is installed.

Release validation on 2026-10-23 passed: 110 Conversations Bun tests, all 114
Playwright browser tests, consumer type checks, panel/frontend builds, the full
`GOWORK=off go test -race -tags=integration ./...` suite, and the application
build with the released App SDK v0.104.0. The platform host/dock checks passed
30 tests separately; no platform source or installed agents were changed.

## Voice transcript revisions (v0.24.51)

`TestVoiceTranscriptCapturedSessionRevisions` replays the actual shape of the
2026-10-23 GPT Live session: 13 cumulative checkpoints plus two six-event shutdown
batches. It requires six stable message identities, preserved creation times,
live revision publication, durable change-cursor recovery and clean context for
a subsequent typed turn. No transcript event may dispatch an inbound agent turn
or create a transport delivery.

Additional race tests cover concurrent out-of-order checkpoints, duplicate
deliveries, provider-final precedence, blocked transcript redaction, session/chat
isolation and expired ownership. The pre-existing legacy voice test still
exercises events without transcript metadata. Pure UI and built-bundle browser
tests verify prefix replacement, readable adjacent fragments, reload, shutdown
revisions and blocked speech across dashboard, app-served and package surfaces.

These regressions use captured event structure and isolated fixtures, without
opening a microphone or calling the paid voice provider. No Core, Server,
provider settings or existing conversations are modified by the release.

Release validation on 2026-10-23 passed: 113 Bun UI/client tests, all 117
Playwright tests (plus the three voice cases rerun with blocked-history reload),
frontend builds and consumer type checks, the complete Go race/integration suite
(96.1 seconds), and a production application build with the pinned released SDK.

## Full duplex voice in the real browser (paid Tier 3)

```sh
APTEVA_RUN_CONVERSATIONS_VOICE_TIER3=1 GOWORK=off apteva test --tier 3 \
  --provider openai --model gpt-6.1-sol --json scenarios/22-voice-duplex-browser.yaml
```

Supply `OPENAI_API_KEY` through the runtime environment. This requires the CLI
runner that provisions the project-scoped `openai-api` connection; older runners
incorrectly rely on Server environment bootstrap. Select current local binaries
with `APTEVA_SERVER_BIN` and `APTEVA_CORE_BIN`. No existing user agent is reused.

The native runner owns the disposable Server, project, agent and Conversations
sidecar. The driver synthesizes French microphone audio using OpenAI TTS and
feeds it through a real Chromium MediaStream and the production capture worklet.
The production VoiceAudioClient connects through Server's audio relay to Core
and real `gpt-live-1`. It interrupts a spoken explanation with a new appointment
request, verifies scoped clearing/receipts, continuous microphone capture,
subsequent consumed audio, the correct French answer in durable history, actual
model telemetry, and absence of missing-caption stops, errors and disconnects.
The 65-second startup wait accommodates Server's known agent telemetry ownership
refresh interval. API credentials are never included in browser code or output.

This is real Web Audio scheduling with a synthesized microphone, not a simulated
playback clock. It does not establish acoustic echo cancellation quality on every
physical device. Deterministic unit/browser regressions additionally cover A/B
cancellation, late PCM/callbacks, partial progress, legacy controls, silence,
noise/onset debouncing, mute, and stale connection callbacks.

## Reused provider tool-call IDs (v0.26.1)

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.1-sol \
  --json scenarios/27-reused-tool-call-identity.yaml
```

The native runner owns the server, Conversations sidecar and real model agent.
The driver inserts a loopback proxy at that sidecar boundary, preserves trusted
agent/thread/project headers, replaces every write's tool-call ID with `call_0`,
and replays the exact request before returning its original receipt. Core and
the provider remain unchanged. Two identical replies to separate user inputs
must both persist; report, alert and approval cards must remain distinct; each
injected retry must return the same message ID without another insertion.
The driver serializes new user submissions with each injected retry. A queued
follow-up can still exist before a final tool call and be released by that call;
this transition must not change its retry identity.

Deterministic regressions cover legacy rows with `tool-call:<agent>:call_0`,
acknowledgement/progress/final messages, all three typed Inbox tools, questions,
forms, queue-state transitions, store-wrapper recreation, and explicit-key
conflicts. Automatic keys use the trusted call ID, full normalized payload
(which includes agent/thread/conversation) and latest accepted user-message ID.
There is no schema migration or lookup against old automatic keys.

The SDK does not expose a separate execution identity. Automatic suppression
is therefore scoped to the accepted input context: an identical invocation
retried after genuinely new user input is indistinguishable from new work.
Callers needing retry identity across that boundary should supply an explicit
`idempotency_key`; its durable duplicate/conflicting-request checks are unchanged.

## Form answer shows live work (0.34.4)

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.2-sol \
  scenarios/29-form-answer-live-work.yaml
```

2026-11-25: passed in 111.6 s (9 iterations, 173K tokens) with the fix. With
0.34.3's `interactions.go` the same run fails as prod did: "no Thinking in the
conversation between the answer and the final reply" (only text frames, no
response progress after the answer).

## Thinking ends after reply and pace (0.34.5)

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.2-sol \
  scenarios/30-thinking-ends-after-reply.yaml
```

Prod 2026-11-25 (Helper, its own chat thread): after the final reply the agent
called Core's `send` to main and `pace clear_wake` in one step. Core fed the
send result back in one more model call, which reopened Thinking; nothing closed
it and the operator's next message was queued. The exact sequence is replayed in
`TestClearWakePaceAfterReplyLeavesNoThinking` (fails on 0.34.4, passes on
0.34.5), with `TestTimedPaceWakesOnlyAfterItsDelay` and
`TestInputDuringTimedPaceShowsThinking` for timed paces and new input.

Scenario 30 guards the same contract end to end: reply, `pace clear_wake`,
Thinking stays off for 15 s, and the next message is released and answered.
gpt-6.2-sol did not repeat the prod batch (send + pace in one step) in five
runs even when told to, so the scenario passes on 0.34.4 too; the Go test is the
regression check. Separately, after a chat thread starts, Core sometimes keeps
retrying empty Codex responses on main until the runner's timeout ("scenario
cancelled" with a passing driver); that is Core, not Conversations.

2026-11-25 full Tier 3 on 0.34.5 (openai-codex / gpt-6.2-sol, 3 lanes): 27 of
28 runnable scenarios passed on the first run; 17 and 22 are opt-in
(`APTEVA_RUN_CONVERSATIONS_NATIVE_IMAGE_TIER3`, `APTEVA_RUN_CONVERSATIONS_VOICE_TIER3`)
and were not run. Scenario 21 failed once on the suggestion check (model
produced no suggested reply), then its driver passed on rerun; its
`code_read_file` trajectory clause was dropped because gpt-6.2-sol reads files
with `repos_run_command`. `go test -race ./...` passes; it caught a timing flaw
in 0.34.4's `TestAnsweredFormShowsMainWorkInTheConversation` (events dated
before the answer opened the response), fixed in the test.

## One send tool for replies, alerts and reports (0.38.0)

`conversations_send` takes `kind=message|alert|report` and an optional `topic`;
`conversations_alert` and `conversations_report` are aliases. Go coverage:
`send_kinds_test.go` (kinds, topic reuse/creation, a conversation thread's own
conversation, public refusals, aliases, no reply streaming for cards, the
`io.apteva/autoload: "named"` hints, and a size budget for `send`).

Scenarios 06, 08, 24 and 27 now assert `tool_called_with: conversations_send`
with `args: {kind: alert}` / `{kind: report}`. Scenario 31 is new:

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.2-sol \
  scenarios/31-scheduled-triage-files-cards.yaml
```

An email-triage agent (directive modeled on the prod Messaging Agent) whose
main thread already answers an operator chat gets an hourly check and a daily
summary as scheduler events. It must file an alert card and a report card, not
plain messages. 0.37.0 passes its driver too, on today's local Core and on the
Core from before the tool-selection fixes: the prod failure came from a tool
selection that dropped the alert/report tools after restarts, which a fresh
scenario does not reproduce. The scenario guards the card path; the merge
removes the separate tools that selection could drop.

### Results on the model-driven-tools Core (2026-11-27)

Local Core with model-driven tool loading (apteva/core `feat/model-driven-tools`
plus `fix/turn-waits-for-tools`): every tool goes to the model while the catalog
fits 30K schema tokens. Each run waited for 4 GiB of free disk and was retried
once if it failed on a full disk; no retry was needed.

kimi-k3.1-pro (opencode-go), full suite on the same Core: every scenario
passed on its first run except 14 and 21. In 14 Kimi filed the right report in
the right conversation, but its summary began "Ownership-reviewed:", and the
driver matched its markers case-sensitively; they now match in any case, and 14
passed twice after that. In 21 Kimi's startup turn fixed the repository before
any user message: the directive now says to work only on what the user asks.
With that line, 21 passed 3 of 3 with kimi-k3.1-pro (16 iterations, 471–480K
tokens) and 2 of 2 with gpt-6.2-sol (15 iterations, 381K tokens).

gpt-6.2-sol, full suite (17, 22 and 32 are opt-in): every scenario passed. 23 failed
once because the model repeated the `LAUNCH_DONE` token in an acknowledgement
(the driver counts messages containing it) and passed on rerun; 25 and 26
passed once Playwright was reinstalled in the worktree root.

Twelve key scenarios, one run each, 0.37.0 against this version on the same
Core. Both versions passed all of them except 21 (see below):

| | Iterations | Tokens | Card scenarios 06/07/08/24/31 |
|---|---|---|---|
| gpt-6.2-sol | 71 → 68 | 1,257K → 1,174K (−6.6%) | 566K → 483K (−14.6%) |
| kimi-k3.1-pro (opencode-go) | 84 → 77 | 1,649K → 1,484K (−10.0%) | 710K → 643K (−9.4%) |

The alert and report flows (06, 08) take 3 iterations instead of 4 with Sol:
main posts by `topic` and no longer calls `conversations_create` first.

Scenario 21 failed on both versions, both Cores and both models before its
directive asked for a next step: the diagnosis was right every time and no
suggestion was attached. Unprompted (now scenario 32), gpt-6.2-sol went 0 for 6
with 0.37.0 (3 runs on the old local Core, 3 on the new one) and 0 for 6 with
this version (2 on the old Core, 4 on the new one, 2 of them forced into search
mode with `APTEVA_DIRECT_TOOL_TOKENS=4000`). Neither the Core nor the shorter
`send` description explains it; the scenario was written against gpt-6.1-sol. With the directive line, on the
local dev Core (`dev-build/turn-waits-20261127`), 21 passed 5 of 5 with
gpt-6.2-sol (12–13 iterations, 304–331K tokens) and its driver passed 5 of 5 with
kimi-k3.1-pro (15–17 iterations, 446–509K tokens). The first Kimi run went over
the old 500K budget, which is now 750K.

## Helper UI (0.39.0)

Go coverage in `worker_ui_test.go`: a helper's title is its spawn `_reason`
and its running step carries that step's reason; an ended helper sums its
tokens and tool calls; stopping a helper stops its subtree, records it as
stopped by the operator and tells the parent thread; Agent work carries the
helper title and the worker id that opens its detail; a response's duration
and tokens are kept on its last message.

Browser coverage in `frontend/tests/workers.browser.ts` (12 tests): one
group line with one row per helper at 1280 and 390px, a single helper as one
row, thread-id titles when no reason was given, eight-step helpers showing
only their latest activity, an open response waiting on helpers, French,
Agent work titles and paused state, the detail in the side column (1280) or a
drawer (390) with Stop, and the "Worked for" footer.

Scenario 33 is new:

```sh
GOWORK=off apteva test --tier 3 --provider openai-codex --model gpt-6.2-sol \
  scenarios/33-helpers-announced.yaml
```

The operator asks for two helpers: one marks PAID invoices done, the other only
reads a support list. The trajectory must have a `conversations_send` before
the first `spawn`; the driver checks that an agent message sent before the first
helper started names the count and what the helpers won't change, that each
helper has a short title that isn't its directive, that only the PAID todos
were completed, and that the final answer names both refund requests.
