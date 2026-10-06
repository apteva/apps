# Native runner validation — September 13, 2026

All Conversations tests now have one entry point: `apteva test`. The migration
keeps Go unit/integration tests, adds the UI/type/browser commands via
`apteva.test.yaml`, and registers all 14 live workflows as native YAML scenarios.
Client drivers retain the original multi-step HTTP/SSE assertions; they no longer
bootstrap a server, install apps or create/reuse personal agents.

## Actual results

- CLI runner tests: passed, including race detection, process cancellation and secret redaction.
- Tier 1 frontend: 31 Bun tests passed; public composer type checks passed.
- Tier 2 frontend: all 32 browser tests passed (dashboard and exported chat).
- Go Tier 1 and Tier 2: failed on the existing `TestHTTPAppContextPreservesMountedContext` assertion (`browser request must derive a separate context`). No other Go failure was reported.
- Tier 3: all 14 workflows executed, 12 passed and 2 failed. No live test was skipped.
- Model: real `openai-codex` / `gpt-5.6-terra`, using the local authenticated connection at runtime. The runner created disposable servers and reused the existing Core executable.

| Scenario | Outcome | Seconds |
| --- | --- | ---: |
| chat-round-trip | PASS | 7.38 |
| single-conversation-round-trip | PASS | 8.22 |
| soft-break | PASS | 42.76 |
| image-storage-ticket | FAIL | 30.37 |
| two-conversation-isolation | PASS | 11.38 |
| alert-flow | PASS | 91.50 |
| approval-round-trip | PASS | 163.92 |
| report-flow | PASS | 82.95 |
| public-visitor-escalation | FAIL | 140.73 |
| public-self-serve | PASS | 99.85 |
| public-refusal | PASS | 91.74 |
| room-fanout-and-retry | PASS | 7.39 |
| conversation-approval-destination | PASS | 16.28 |
| preserves-conversation-ownership | PASS | 57.57 |

The image row uses the final isolated rerun with the completed multi-app configuration.
The initial whole-suite image attempt failed because the Tickets MCP was not
spawnable. That runner setup issue was corrected; both subsequent image runs
created a ticket and its Storage attachment but failed the visual assertion.
The other rows are from the complete suite run. The first chat-only bootstrap
also caught and corrected the runner's project-scope assumption for global apps.

## Image regression

The final scenario provisions local Conversations, Storage and Tickets, enables
`attachment_storage`, and binds both apps to the same Storage installation.
The test confirmed:

- An uploaded message exposes a positive Storage file ID.
- Retrying the identical client message reuses its message ID and Storage file ID.
- Exactly one uniquely titled ticket and one attachment are created.
- The actual attachment tool receives that exact file ID in the originating chat thread.
- Downloading the Storage file returns byte-for-byte the original PNG.
- No attachment-reading tool is called by the model.

**Failure:** the final fixture was green; the ticket description said blue.
A preceding corrected run also misidentified a red fixture as blue. This is not
a passing vision test just because the Storage and Tickets steps succeeded.

Final fixture SHA-256: `a0fbbaf57601c16f9110c1290be458f56608cc913cf87da9c3eaf8bd6e7369eb`.
Telemetry in the preceding run records `attachment.consumed` at iteration 1
with reason `provider_request_completed`, before the later ticket tool calls.
This is consistent with the previously reported image-retention concern; the
migration does not change Core or claim to fix its vision behavior.

Coverage boundary: this exercises uploaded attachment IDs. Legacy inline
`data_url`-only submissions without IDs and binding-only auto-enablement remain
outside this test; the explicit Storage toggle is required.

## Public escalation regression

The visitor received a response and the public-chat guard correctly rejected
an approval in the visitor conversation. Main then called
`conversations_request_approval` with the Core thread ID (`chat-conv-…`)
instead of a Conversations ID (`conv-…`). The app rejected it as not found.
No operator escalation appeared within the preserved 120-second deadline.
This is a failing agent workflow, not evidence of an authorization bypass.

## Evidence and reproduction

See [TESTING.md](TESTING.md) for the unified commands and credential setup.
The CLI checkout needs the scenario-driver and native-command additions; no
CLI/app release is claimed by this migration. The tested CLI binary was
`/private/tmp/apteva-conversations-test`.

Local evidence (credentials are not written into these commands):

- `conversations-validation-runtime/evidence/native-scenarios.log` — complete 14-scenario run.
- `conversations-validation-runtime/evidence/native-04-image-storage-ticket.log` — final image run with byte/ID/vision assertions.
- `conversations-validation-runtime/evidence/native-04-image-storage-ticket-vision-failure.log` — earlier red/blue failure.
- `conversations-validation-runtime/evidence/native-all-checks.log` — Go, Bun, types and browser checks through the runner.
- `conversations-validation-runtime/evidence/native-scenarios/` — individual failure results and telemetry.

Conversations production code, Core, Flexylead, app manifests/dependencies and
published versions were not changed. Pre-existing CLI work was retained.
