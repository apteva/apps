# Processes 0.5.0

Processes can listen for business events from another installed app, map event
fields into assignment parameters, start a run, and dispatch ready steps to the
assigned agents. Tasks remains optional.

- Assignment event triggers select a source installation and event topic, with
  typed conditions and parameter mappings.
- The panel supports paused creation, activation/pause, listener status,
  read-only previews, explicit test runs, event history, and failed-event retry.
- MCP and HTTP APIs provide the same trigger configuration and inspection.
- Duplicate event delivery creates one run. Event receipts and run reservation
  commit together; saved snapshots retain the rules, source event, and inputs.
- Pausing a process, assignment, or trigger invalidates obsolete deliveries.
  Events published while unsubscribed are not backfilled on resume.
- Existing dependencies, parallel steps, role bindings, and approval gates govern
  agent handoffs. Schedules and direct execution remain available without Tasks.

## Platform requirement

Event triggers require the app-target event subscription server APIs merged in
[server PR #3](https://github.com/apteva/server/pull/3), commit
`093ed35cb6dfc1f82c4586101b38d0e2eabc580d`, and published app-sdk v0.80.0.
The manifest declares Apteva 0.51.3 as the minimum target for this release.
At publication preparation, the latest stable Apteva version is 0.51.2, which
does not contain those server APIs. Use an updated server build containing that
commit; a version number alone is not a capability check. Older servers may not
enforce the manifest minimum. This app release does not install or upgrade the
platform. The panel reports listener synchronization failures; a trigger is live
only after its subscription is confirmed.

Publishers should reuse a stable event ID when retrying publication. Without
one, separate publications are separate business events. Failed-event retries
are explicit and preserve the original failure. No external publishing service
is part of the test fixture.

## Validation

The complete Processes Go race suite, 24 UI/verifier tests, strict TypeScript,
panel bundle/import checks, and desktop/mobile browser checks passed.
The real `openai-codex` / `gpt-5.6-terra` scenario published one signup twice
through a separate app and the platform bus: exactly one five-step run completed
across three agents, including parallel inputs, a dependency join, and explicit
approval. Saved state and agent-attributed tool traces passed verification:
30 iterations, approximately 144 seconds. Final publication was simulated.

See [event trigger documentation](docs-event-triggers.md) and
[Tier 3 scenarios](scenarios/README.md) for configuration and reproduction.
