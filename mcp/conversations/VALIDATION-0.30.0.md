# Conversations v0.30.0 validation — 2026-11-24

Agents put text meant to be pasted elsewhere (prompts, commands, configs) in
fenced code blocks. Those rendered as a plain bordered `<pre>`: no copy
button, every long line cut off at the right edge, the only sideways
scrollbar at the bottom of a box that could be taller than the screen, and
nothing saying what the block was.

Change (frontend only; no API or data change):

- `renderSafeMarkdown` now runs `decorateCodeBlocks` on the sanitized HTML.
  Each `<pre>` is wrapped in `.chat-code` with a header (the fence's
  language, or "Text"; a wrap toggle; Copy) and, for blocks over 18 lines or
  1,400 characters, a "Show all N lines" footer that starts collapsed
  (22rem, faded).
- Unlabelled, `text`, `txt`, `plain`, `plaintext`, `markdown`, `md` and
  `prompt` blocks wrap. Other languages keep their lines and scroll
  sideways. The toggle switches either way (`aria-pressed`).
- One delegated document listener drives the buttons, using
  `composedPath()` so it also works inside a shadow root. The controls are
  added after sanitizing and carry no inline handlers. Copy writes the
  block's text exactly as written (minus the newline marked appends) with
  the Clipboard API, falling back to a hidden textarea and
  `execCommand("copy")` on plain-HTTP hosts. It shows "Copied" or "Copy
  failed" for 1.6s.
- The header is sticky, so Copy stays reachable while scrolling an expanded
  block. A 16px shadow in the page background covers the transcript's top
  padding above it.
- Labels come from the conversation's locale (new `code.*` keys, English,
  French and Spanish) via `useCodeBlockLabels`. Used by agent messages,
  streaming replies, interactive question text and report cards.

Not covered:

- A streaming reply re-renders its HTML on every token, so wrap/expand
  choices made on a block that is still streaming reset until the reply
  completes.
- On a collapsed block that does not wrap, the sideways scrollbar is hidden
  under the fold; trackpad/shift-wheel scrolling still works, and expanding
  shows it.
- Real iOS Safari and Android Chrome (clipboard permission prompts).

## Checks

- Playwright (`bunx playwright test`): 187 passed. New
  `code-blocks.browser.ts`, at 1280px and 390px:
  - Three blocks get headers: unlabelled → "Text", `go`, `sh`. Inline code
    stays inline.
  - The unlabelled block wraps (no sideways overflow); `go` does not, and
    at 390px overflows sideways until "Wrap lines" is pressed
    (`aria-pressed=true`), then fits.
  - The 47-line block starts under 400px tall; "Show all 47 lines" grows it
    by more than 200px and flips to "Show less" (`aria-expanded=true`).
  - Copy (Chromium, clipboard permission granted) puts exactly the agent's
    text on the clipboard for the prose block and for the `go` block
    (with its line breaks and tab), shows "Copied", and returns to "Copy".
  - The sticky header stays on screen with the expanded block scrolled to
    its middle; the page never scrolls sideways.
  - With `locale=fr`: "Texte", "Copier", "Afficher les N lignes".
  - `chat.browser.ts`'s theme test now checks the border on `.chat-code`
    and that its header is sticky (it checked the `<pre>` border, which
    moved to the wrapper).
- Screenshots at 1280px and 390px, collapsed and expanded: the fade and
  footer, the header stuck under the chat header, no text showing above it.
- 148 Bun UI/client tests passed (2,099 assertions); `tsc --noEmit` and
  the consumer TypeScript check passed.
- `GOWORK=off go test -race -tags=integration ./...` passed (165.2s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- App SDK stays pinned at v0.115.2.
