# Conversations v0.27.5 validation — 2026-11-23

Reported on prod (agents.schwartzindustries.com, install 165): a Tasks agent
reply showed every todo as "1. 1. Define the goal...", "2. 2. ...". The stored
message (2462) reads `1. 01. Define the goal and scope (Nov 27). ...`: the
agent numbered the list and also kept the `01.` sequence prefix of the todo
titles. CommonMark reads an item that starts with `01. ` as a nested ordered
list, so the app's `marked` 18 (GFM) renders `<ol><li><ol><li>Define...`, and
the inner list renumbers `01` to 1. GitHub renders it the same way. It was the
only agent reply on prod with that pattern.

This is not a renderer bug, so rendering is unchanged. The rule now reaches
every agent that writes to a conversation:

- `conversations_send` gives its `text` field a description: Markdown; the list
  supplies the number (`1. Define the goal`, never `1. 01. Define the goal`);
  use bullets for items whose titles already carry numbers. Tool descriptions
  are visible whenever the tool is, including to agents that never load the
  skill.
- The `using-conversations` skill's portable-text paragraph states the same
  rule and why.

## Checks

- New `TestNumberedListRuleReachesAgents` checks the send tool's `text`
  description and the skill body. It fails when the rule is removed from the
  tool description (checked, then restored).
- `GOWORK=off go test -race -tags=integration ./...` passed (166.2s).
- `go vet` clean, including `-tags 'integration scenario'`.
  `GOWORK=off go mod tidy` left go.mod/go.sum unchanged.
- 142 Bun UI/client tests passed, with 2,044 assertions; package build and
  consumer TypeScript check passed. Frontend source unchanged (version literals
  only), so Playwright was not rerun.
- Tier 3 (real-model scenarios) not run: no Codex test credentials here, so
  whether models follow the new rule is not measured.
- App SDK stays pinned at v0.115.2.
