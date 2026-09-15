# Changelog

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
