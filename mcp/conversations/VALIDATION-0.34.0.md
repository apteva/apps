# Conversations v0.34.0 validation — 2026-11-25

Helper cards, "Waiting for N helpers…" and the Agent work section in Details,
built on the thread observation API (app-sdk v0.122.1, server#104).

## Checks

- Go (`workers_test.go`, fake `ThreadObserver`):
  - `TestWorkersLinkBySpawnSpanAndInheritToNestedWorkers`: a spawn call starts
    one watcher (project, agent, 24 h of ended threads); a second spawn reuses
    it. A thread whose `spawned_by.tool_span_id` matches the spawn row links to
    that conversation with the spawn call id; its own helper links through it
    at depth 2; main's unrelated helper is ignored. Both publish `worker`
    frames. The same snapshot again changes no revision. A finished helper
    keeps its result; a helper missing from a complete list becomes `stopped`
    / `lost`.
  - `TestWorkersFallBackToRequestedThreadIDWithoutSpan`: without a span the
    spawn's requested `id` links it.
  - `TestWorkersNeverLinkToPublicConversations`: a public conversation's
    spawn starts no watcher and is never linked; `/workers` returns `[]` and
    `/agent-work` 404.
  - `TestWorkerWatcherStopsWhenNothingIsOpen`, `TestResumeWorkerWatchesAfterRestart`.
  - `TestWorkerActivityRouteChecksLinkAndDetail`: a worker not linked to the
    conversation is 404; a refused content projection falls back to metadata;
    arguments and results appear only with `detail=1`.
  - `TestAgentWorkNamesConversationThreads`.
- Playwright (`tests/workers.browser.ts`, 1280 and 390 px, terminal theme):
  live card (name, "Using web search", timer, "+1 helper"), expanded steps
  ("Thinking · 2.1s", "Reported to main: …", "Ended with the helper",
  folded arguments/output), nested card, result with "Show full", polite
  announcement, no horizontal scroll, survives reload; last eight steps then
  "Show earlier"; "Waiting for 1 helper…" replaces "Thinking…" and goes back
  when the helper finishes; French labels; Agent work (main next wake, this
  conversation highlighted, helper with its parent, ended folded, another
  conversation opens on click); paused banner; "Show helper steps" opens cards.
- Full suites: `GOWORK=off go test -race -tags=integration ./...`, `go vet`
  (also `-tags 'integration scenario'`), Bun 148 (app root) and 49
  (frontend), Playwright 198.

## Not covered

- "Waiting for N helpers…" counts unfinished helpers the answering thread
  started; it does not read the answering thread's own state, so a thread in
  a model turn while its helper runs also shows the waiting line.
- The topology's `model` is still the model tier on current servers, so the
  card shows "Model: large" rather than a model id.
- Prod servers without server#104 have no thread observation: spawn rows stay
  as before and Agent work says the activity is not available.
