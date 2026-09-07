# Conversations 0.20.0 validation

Validated September 7, 2026 against an isolated local platform with real
OpenAI Codex credentials and temporary agents. Production was not changed.
App source was built without a workspace overlay using Go 1.26.8 and
app-sdk v0.73.0. Frontend validation used published Web SDK 0.6.0 and React 19.2.8.

## Automated checks

- 134 Go unit/integration tests passed with race detection; Go vet passed.
- 25 frontend tests passed (117 assertions), including saved draft migration,
  client identity replacement, stable send retries and SSE/REST cursor separation.
- Four Playwright fixture tests passed: chat and inbox/report/approval UI in both
  dashboard and external wrappers, including desktop/mobile sizing.
- Strict TypeScript and all three production dashboard bundles passed.
- The packed frontend package installed, type-checked and bundled in an independent
  consumer project, including stylesheet and component registry imports.
- govulncheck and the frontend consumer dependency audit reported no vulnerabilities.

## Real platform and browser checks

Two real platform-issued application-user tokens sharing an issuer were used to
create separate conversations with the same key. Cross-user history, send and read
marks were denied; Telegram administration was denied. A repeated send reused its
message ID; real Codex answered and authenticated SSE connected.

The production frontend package and dashboard wrapper were exercised in Chromium
against that platform using a local host harness. External requests crossed origins
with bearer authentication, passed CORS, and omitted cookies. Dashboard requests
used a same-origin proxy and the existing session cookie. Both hosts sent messages
and rendered real Codex replies. A second visitor could not see the first visitor's
transcript. Desktop (1280px) and mobile (390px) screenshots were inspected.
The harness is not a full navigation test of the entire dashboard application.

## Tier 3 — real Codex

The final candidate passed all 12 workflows in 218.555 seconds:

| Workflow | Result |
| --- | --- |
| CodexChatRoundTrip | Pass (4.29s) |
| CodexSingleConversationRoundTrip | Pass (3.28s) |
| CodexSoftBreak | Pass (12.35s) |
| CodexTwoConversationIsolation | Pass (20.32s) |
| CodexAlertFlow | Pass (16.30s) |
| CodexApprovalRoundTrip | Pass (44.36s) |
| CodexReportFlow | Pass (20.37s) |
| CodexPublicVisitorEscalation | Pass (24.69s) |
| CodexPublicSelfServe | Pass (28.55s) |
| CodexPublicRefusal | Pass (24.41s) |
| CodexRoomFanoutAndRetry | Pass (6.81s) |
| CodexConversationApprovalDestination | Pass (12.39s) |

An earlier complete run also passed all 12 workflows before the final aggregate
scope-filter correction. The final run above includes that correction.

## Reproduction

From `mcp/conversations`, run `GOWORK=off go test -race -tags integration ./...`
and `go vet ./...`. From the repository root, run the commands in
`frontend/README.md`. Build `frontend` before its browser tests, which import the
built public entry points.

For real Codex, configure a disposable platform/project with Codex credentials,
install this app, enable automatic app attachment for new agents, and run:

```sh
APTEVA_BASE_URL=... APTEVA_API_KEY=... APTEVA_LIVE_PROJECT_ID=... \
  GOWORK=off go test -tags live -run '^TestLive_' -v -count=1 -timeout 30m
```

Keep credentials in the environment. The live suite creates temporary agents and
conversations and removes them on completion. Browser fixture credentials only
apply to their controlled test service and do not prove platform authorization.
