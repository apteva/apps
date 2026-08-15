# Prospecting

Prospecting is Apteva's standalone workspace for building and reviewing a lead
catalog. It works immediately with manually added or imported data. Optional
integrations extend it with browser-backed discovery and CRM handoff.

## Ownership and integrations

Prospecting owns:

- target profiles;
- manual creation and CSV/JSON import;
- bounded discovery runs;
- candidate companies and people;
- deterministic noise filtering and qualification;
- extracted contact, location, team-size, and workflow signals;
- explainable fit and confidence scores;
- Web evidence references;
- review decisions and exclusions;
- portable CSV/JSON export;
- optional CRM handoff references.

Optional integrations add:

- browser search, page extraction, and raw artifacts through `web`;
- accepted-contact ownership and duplicate detection through `crm`.

Neither app is required to install or use Prospecting. Prospecting does not use
an AI model. It does not send messages, make calls, create
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

1. Add leads manually or import up to 1,000 rows of CSV or JSON. If no target
   profile exists, Prospecting creates an `Imported leads` profile.
2. Search, filter, edit, defer, reject, and export the catalog without another
   app.
3. When Web is connected, create a target profile and run
   `prospecting_search_run`. Google automatically falls back to DuckDuckGo
   when blocked, and known directories, marketplaces, social results, and form
   templates are filtered.
4. Run `prospecting_candidates_qualify` or the bounded batch variant. The app
   prioritizes contact and identity pages across up to five first-party pages,
   extracts structured facts, detects automation opportunities, classifies
   eligibility, and recalculates scores. Repeated batch calls advance through
   candidates that have not yet been enriched.
5. Review the candidate, rule explanations, and saved source evidence.
6. Optionally use `prospecting_candidates_research` for broader cited research.
7. Add or correct decision-maker details, then reject or defer.
8. When CRM is connected and the user explicitly requests it, send the lead to
   CRM. This upserts the person idempotently and never sends a message.
