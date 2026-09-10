# Changelog

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
