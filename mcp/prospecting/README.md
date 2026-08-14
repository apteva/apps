# Prospecting

Prospecting is Apteva's Web-first candidate discovery and qualification app.
It follows the same orchestration pattern as Commerce: the app owns its domain
workflow and delegates specialized work to authoritative sibling apps.

## V1 boundary

Prospecting owns:

- target profiles;
- bounded discovery runs;
- candidate companies and people;
- explainable fit and confidence scores;
- Web evidence references;
- review decisions and exclusions;
- idempotent CRM handoff references.

Prospecting delegates:

- browser search and research to `web`;
- accepted contacts and duplicate detection to `crm`.

V1 does not send messages, make calls, create opportunities, operate campaigns,
or execute paid lead-data providers.

## Local development

```sh
go test ./...
go build .
cd ../..
bun run scripts/build-panels.ts --app prospecting
```

The workspace-root `go.work` overlays the local `app-sdk`. Standalone builds
use the version pinned in `go.mod`.

## Core flow

1. Create a target profile.
2. Run `prospecting_search_run` to search through Web.
3. Review a candidate and its source evidence.
4. Use `prospecting_candidates_research` for deeper cited research.
5. Add or correct decision-maker details.
6. Reject, defer, or accept the candidate.
7. Acceptance upserts the person into CRM and never sends a message.
