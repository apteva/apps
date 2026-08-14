# Prospecting

Prospecting is Apteva's Web-first candidate discovery and qualification app.
It follows the same orchestration pattern as Commerce: the app owns its domain
workflow and delegates specialized work to authoritative sibling apps.

## v0.2 boundary

Prospecting owns:

- target profiles;
- bounded discovery runs;
- candidate companies and people;
- deterministic noise filtering and qualification;
- extracted contact, location, team-size, and workflow signals;
- explainable fit and confidence scores;
- Web evidence references;
- review decisions and exclusions;
- idempotent CRM handoff references.

Prospecting delegates:

- browser search, page extraction, and raw artifacts to `web`;
- accepted contacts and duplicate detection to `crm`.

v0.2 does not use an AI model. It does not send messages, make calls, create
opportunities, operate campaigns, or execute paid lead-data providers.

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
2. Run `prospecting_search_run`. Google automatically falls back to DuckDuckGo
   when blocked, and known directories, marketplaces, social results, and form
   templates are filtered.
3. Run `prospecting_candidates_qualify` or the bounded batch variant. The app
   prioritizes contact and identity pages across up to five first-party pages,
   extracts structured facts, detects automation opportunities, classifies
   eligibility, and recalculates scores. Repeated batch calls advance through
   candidates that have not yet been enriched.
4. Review the candidate, rule explanations, and saved source evidence.
5. Optionally use `prospecting_candidates_research` for broader cited research.
6. Add or correct decision-maker details, then reject, defer, or accept.
7. Acceptance upserts the person into CRM and never sends a message.
