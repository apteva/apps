# Conversations v0.28.0 validation — 2026-11-23

The composer looked cramped on phones, in the Helper dock and in other narrow
chats. Those use the compact layout (set explicitly by the dashboard docks, or
picked by `auto` at 480px or narrower). Compact put `+`, the microphone, the
text and send on one row, so a long draft wrapped in a narrow, indented column
between the buttons. The target was Intercom's composer: the draft on top at
full width, with a slim tools row beneath it.

Change (CSS plus one wrapper handler; no API or data change):

- Every layout except `single-line` stacks: the draft row
  (`.chat-composer-entry`) spans the box, and the tools row
  (`.chat-composer-toolbar`) holds `+` and the mic on the left and send/pause
  on the right (`margin-left:auto`).
- Expanded keeps a roomier box. The extra height (min 66px) sits on the
  entry wrapper, not the textarea, so the textarea's measured height always
  follows its text. Compact and narrow `auto` use one text line (24px) that
  grows to 144px as you type, with 32px buttons.
- The entry wrapper focuses the input when its empty padding is clicked, so
  the taller expanded box behaves like one field.
- Send is a grey button until there is something to send, instead of a faded
  accent. The box gets a soft ring on focus.
- Phones (≤640px) keep the composer 12px from the bottom edge (or the
  home-bar inset plus 8px) instead of 24px.
- `single-line` is unchanged: one fixed row (`display:contents` on the
  toolbar keeps its buttons in the box's row).

## Checks

- Playwright (`bunx playwright test`): 182 passed, across the dashboard,
  external, panel and package hosts. Updated assertions:
  - Narrowing an `auto` chat from desktop width to 420px keeps the draft on
    its own row above the tools; the draft spans the box; the box gets
    shorter than at full width and stays under 90px.
  - Explicit `compact` (1280px) and `expanded` (390px) both put the tools
    under the draft. Compact stays under 90px; expanded stays above 95px.
  - Narrow docks at 500px and 320px: in compact, the draft spans the box and
    add, mic and send share the row beneath it, with send on the right. In
    `single-line`, add, mic, input and send still share one row. An empty
    draft is one 24px line in both, even when the placeholder wraps. No
    horizontal scroll.
  - Phone (390px) bottom gap is 11–16px; desktop keeps at least 23px.
- In the example host's browser preview (375px and 1280px): measured box and
  textarea heights while typing, narrowing and growing to three lines
  (narrow draft: box 79px, input 24px; narrow three lines: 127/72; wide
  empty: 116, with the input stretched to 48 by the wrapper; wide three
  lines: 140/72). Clicking the wrapper's padding focused the input. Checked
  the empty state's grey send and the pause button during a response.
- 148 Bun UI/client tests passed (2,066 assertions); consumer TypeScript
  check passed.
- `GOWORK=off go test -race -tags=integration ./...` passed (166.0s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- App SDK stays pinned at v0.115.2.

Not covered: real iOS Safari and Android Chrome (the keyboard, the home-bar
inset). The emulated phone viewport covers layout only.
