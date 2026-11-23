# Conversations v0.27.7 validation — 2026-11-23

With 0.27.6 the prod Tasks reply rendered as written ("• 01. Define the
concept..."), but every line carried both a bullet and the title's own
sequence number. The operator chose a plain numbered list instead.

Change (guidance only; rendering is unchanged from 0.27.6):

- `conversations_send` `text` description: the list supplies the number;
  never `1. 01. Define the goal`; when item titles carry their own sequence
  numbers (todos named `01. ...`), drop that prefix and use a plain numbered
  list.
- `using-conversations` skill: the same rule with the example
  `1. Define the concept`, not `- 01. Define the concept`.
- 0.27.6's renderer fix stays: if an agent still writes `- 01. ...`, it shows
  as written rather than as a nested "• 1.".

## Checks

- `TestNumberedListRuleReachesAgents` now also requires "drop that prefix and
  use a plain numbered list" in the tool description and the matching skill
  sentence.
- `GOWORK=off go test -race -tags=integration ./...` passed (147.5s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- 147 Bun UI/client tests passed, with 2,062 assertions; package build,
  app-served frontend build and consumer TypeScript check passed. The rebuilt
  app bundle is byte-identical (version literals only), so Playwright was not
  rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here, so
  whether models follow the new advice is not measured.
- App SDK stays pinned at v0.115.2.
