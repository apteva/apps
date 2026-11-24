# Conversations v0.30.1 validation — 2026-11-24

Reported on a real conversation right after 0.30.0: code blocks looked "cut
on top". 0.30.0 gave the sticky code block header a `box-shadow: 0 -16px 0
var(--bg)` to hide the transcript's top padding above a stuck header. The
shadow was always painted, so it covered the block's top border and rounded
corners and the bottom of the line above the block. 0.30.0's screenshots
showed it too (missing top border at 1280px); it was missed.

Change (CSS plus one wrapper element; no API or data change):

- The header is now an outer sticky element (`container-type:
  scroll-state`) holding a `.chat-code-bar` with the language and buttons.
- The 16px strip moved to `@container scroll-state(stuck: top)`, so it is
  painted only while the header is stuck. Browsers without scroll-state
  queries (Safari, Firefox today) never paint it; there, a few pixels of
  text can show above a stuck header, which is the lesser problem.

## Checks

- `code-blocks.browser.ts` now asserts the short `go` block's bar has no
  box-shadow while not stuck (0.30.0 fails this), and, where scroll-state
  queries are supported (Playwright's Chromium: yes), that the tall block's
  bar gets one once its header is stuck while scrolling.
- Screenshot at 1280px of a block under a two-line paragraph: top border,
  rounded corners and the paragraph's last line all visible.
- Playwright: 187 passed. The first full run had 2 failures in
  `thread-actions.browser.ts`; that file passed alone (12/12) and the full
  rerun passed 187/187.
- 148 app Bun tests and 49 frontend Bun tests passed; `tsc --noEmit` passed.
- `go vet` clean; `GOWORK=off go test ./...` passed;
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
