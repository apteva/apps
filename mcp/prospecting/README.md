# Prospecting

Prospecting is Apteva's standalone workspace for building and reviewing a lead
catalog. It works immediately with manually added or imported data. Optional
integrations extend it with Google Places and browser-backed discovery, qualification, and CRM handoff.

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

- structured local-business discovery through the existing `google-places` connector;
- browser search, page extraction, and raw artifacts through `web`;
- accepted-contact ownership and duplicate detection through `crm`.

These integrations are optional. Qualification uses fixed rules and no AI model.
Google Places requests can incur Google Cloud charges. CRM handoff does not
send messages; outreach remains a separately confirmed action.

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
   includes rendered footers and French contact, reservation and legal pages,
   extracts structured facts, detects automation opportunities, classifies
   eligibility, and recalculates scores. Repeated batch calls advance through
   candidates that have not yet been enriched.
   A restaurant-specific mailbox on its own site can use a parent-company
   domain; generic group, agency, press and privacy addresses are excluded
   from that exception. Full-body extraction requires a Web build supporting
   `web_extract(readability=false)` and a Computer build that retains footers
   in that mode. Transient extraction failures retry once within the crawl
   budget; legal pages are skipped when a usable email has already been found.
   If browser pages still provide no usable email, one public HTML source
   check automates the fallback, retaining its own URL and evidence artifact.
5. Review the candidate, rule explanations, and saved source evidence.
6. Optionally use `prospecting_candidates_research` for broader cited research.
7. Add or correct decision-maker details, then reject or defer.
8. When CRM is connected and the user explicitly requests it, send the lead to
   CRM. This upserts the person idempotently and never sends a message.

## Google Places and automated runs (v0.4.0)

1. Connect a Google Cloud API key with Places API (New) enabled in Apteva
   Integrations. Credentials stay in the platform; Prospecting uses its existing
   `google-places` connector through `ExecuteIntegrationToolContext`.
2. Open Prospecting Settings, select that connection, and set the daily Places
   request limit (default 100, range 1–1000, counted per project in UTC).
3. Create a target profile with industries and locations. In Discover select
   Google Places or Web, a query/area, and up to 20 prospects. Optional rectangle
   bounds restrict Places results; supported Places type filters are available
   through the tool's `included_type` argument.
4. Choose website qualification and either review or automatic CRM handoff.
   Automatic handoff requires Web and CRM, eligibility `eligible`, status
   `ready`, both score thresholds, and a usable email or business phone.
5. Read saved run progress, candidate IDs, reasons, errors, and CRM IDs. Failed
   steps can be retried from the panel or `prospecting_run_resume`.

Example agent call:

```json
{
  "profile_id": 8,
  "source": "google_places",
  "query": "staffing agencies in Dallas, Texas, United States",
  "limit": 20,
  "qualify": true,
  "crm_mode": "auto",
  "min_fit_score": 70,
  "min_confidence_score": 60,
  "list_ids": ["ai-consulting"],
  "idempotency_key": "dallas-staffing-2026-10-08"
}
```

`prospecting_run` returns a queued run immediately. Poll `prospecting_run_get`
with its `id`; `prospecting_run_list` lists pipeline runs. The supervised worker
advances saved steps every two seconds. Page results and tokens are checkpointed;
completed qualification and CRM handoffs survive process restarts. Worker leases
prevent simultaneous processing, expire after five minutes on abrupt crashes,
and are released on clean shutdown. Each step has a three-minute deadline.

Places calls use explicit field masks and never request reviews/photos.
Default bounds are three Places requests per run and at most three search pages;
`max_places_requests` can be set to 1–10. Failed requests also count. When a hard
request limit is exhausted, start a new run after addressing the limit; completed
prospects are deduplicated. Search is ranked and does not claim exhaustive US
coverage. `regionCode=US` is a relevance preference; use the query and geographic
restriction to define the actual search area.

Business listing details (name, address, category, status, website, office phone,
coordinates, source attribution, and retrieval time) are saved separately from
editable candidate fields. Place IDs identify branches independently of shared
company domains. Matching existing Web/manual leads are reconciled conservatively;
ambiguous Web results for several existing branches do not create another company
lead. Repeat discovery refreshes listing snapshots and preserves operator edits.
Ratings, reviews, and popularity are not purchasing intent or score inputs.

Google Maps attribution and provider attributions appear beside saved listing
content. Its phone is a business phone, not a verified decision-maker's number.
Prospects without a website stay for review. Website qualification starts at the
business website rather than the Google Maps listing. Manual edits and explicit
clears are protected during scraping and subsequent qualification retries.

CRM uses the existing contact upsert and saved handoff reference. Company-only
leads retain the company display name and blank person fields. Qualification
notes include evidence URLs and business listing context. No message, opportunity,
or campaign is created by a run.

## Validation

```sh
env GOWORK=off go test -race ./...
bun test mcp/prospecting/ui/ProspectingPanel.test.tsx
bun run scripts/build-panels.ts --app prospecting
```

Run the Go command inside `mcp/prospecting`, and Bun commands from the apps root.
Tests cover branch and cross-source deduplication, closed/excluded businesses,
score gates, missing websites, CRM failure/resume, idempotency keys, persisted
pagination, request limits, worker leases, project isolation, protected operator
edits, HTTP routes, and UI controls. Live Places verification needs a connected API
key; fixtures do not make Google requests or send outreach.
