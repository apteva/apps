# Conversations v0.26.1 validation — 2026-11-13

The production code change is limited to Conversations. Core, Server and App SDK
are unchanged. Existing conversation data needs no migration.

## Regression

`TestReusedToolCallIDAcrossMessagesAndInbox` fails against v0.26.0 at the first
new acknowledgement with `client_message_id already belongs to a different
request`. It passes with the new automatic message identity. It includes a
legacy `tool-call:41:call_0` row and verifies distinct lifecycle replies, report,
alert, approval, question and form messages, each with an identical retry.
Additional tests cover identical replies after new input, queued input release
and unchanged explicit-key conflict rejection.

## Real-model Tier 3

- `conversations-reused-tool-call-identity`: passed in 84.264s; 12 iterations; 200,352 tokens. Runtime assertions verified `openai-codex` / `gpt-6.1-sol`.
- `conversations-interactive-components`: passed in 113.260s; 11 iterations; 179,930 tokens. Runtime assertions verified `openai-codex` / `gpt-6.1-sol`.

The reused-ID scenario rewrites trusted tool-call headers to `call_0` at the
runner-owned app boundary. All nine injected write retries returned the original
message ID without inserting another row. Two identical replies to distinct
user inputs and the report/alert/approval cards survived durable history reload.
The first attempt exposed a queued-input transition during final delivery; the
fixed key includes accepted input regardless of queue state, with a regression
for that transition. See TESTING.md for the automatic-key input-context boundary.

The interactive scenario verified optional questions, a late answer delivered
to the owning thread, safe retries/conflicts, and a blocking form that waited
for input and resumed. A 409 from a conflicting second answer was expected.

These are individual successful runs, not a measured reliability rate.

## Other checks

- `GOWORK=off go test -race -tags=integration ./...`: passed (101.870s).
- `bun test ./ui ./frontend/tests`: 142 passed, 0 failed; 2,044 assertions.
- Consumer TypeScript checks: passed.
- App-served frontend and four panel builds/import checks: passed.
- `GOWORK=off go build`: passed. Release manifest, source tag, frontend package
  and app-served frontend version agree on 0.26.1.
