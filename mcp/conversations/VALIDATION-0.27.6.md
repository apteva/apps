# Conversations v0.27.6 validation — 2026-11-23

After 0.27.5, the prod Tasks agent followed the new advice and wrote its todos
as bullets with their titles (message 2483):

```
- 01. Define the concept, audience and rules (Dec 2)
- 02. Research market, competitors and platform/ad policy risks (Dec 4)
```

The dashboard showed "• 1. Define the concept...". CommonMark reads a list
item whose text starts with `01. ` as a nested ordered list, so `marked` built
`<ul><li><ol><li>Define...` and the browser renumbered `01` to "1.". The same
rule caused 0.27.5's `1. 01. Define the goal` → "1. 1.". 0.27.5's advice (use
bullets, keep numbered titles) therefore led straight into it.

Fix, in `frontend/src/markdown.ts` (`renderSafeMarkdown`, used for transcript
text, streamed replies, interactive text and Inbox card Markdown):

- `keepListItemNumbers` escapes a number that starts a list item's text
  (`- 01. Define` → `- 01\. Define`, also `*`, `+`, `N.`/`N)` markers, nested
  items and blockquoted lists), so the item shows its title as written. Fenced
  code (``` and ~~~) is untouched. Ordinary lists and text are unchanged.
- Telegram already prints these lines literally (its formatter only maps the
  bullet), so it needs no change.
- The send tool's `text` description and the skill keep the 0.27.5 rule but
  give the right reason: `1. 01. ...` shows two numbers.

## Checks

- New `frontend/tests/markdown.test.ts` (5 tests): bullets and numbered items
  keep their own numbers with no nested `<ol>`; other markers, `N)` numbers,
  years, nested and quoted items; ordinary lists, `Step 1:`, `1.5 hours` and
  inline numbers are byte-identical; fenced code is left alone. With the
  escaping removed, 4 of 5 fail.
- New `frontend/tests/list-numbers.browser.ts` renders both prod messages in
  the dashboard, panel, exported-chat and package hosts: four list items, no
  nested lists, texts start with `01.`/`02.`. Before the rebuild (old bundles)
  all four failed with the nested items; after it, all passed. Screenshot
  shows "• 01. Define the concept..." and "1. 01. Define the goal...".
- Full Playwright suite: 181 passed, 1 failed
  (`voice-transcript.browser.ts` package host caption ordering, a timing
  test; voice captions do not use the Markdown renderer). Rerun alone with
  `--repeat-each 4`: 48/48 passed.
- 147 Bun UI/client tests passed, with 2,062 assertions; package build,
  app-served frontend build, all four panel build/import checks and the
  consumer TypeScript check passed. `TestUIModuleEntriesAreSelfContained`
  confirms the panel bundles match their sources.
- `GOWORK=off go test -race -tags=integration ./...` passed (198.1s).
  `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here.
- App SDK stays pinned at v0.115.2.
