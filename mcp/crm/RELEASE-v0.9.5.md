# CRM v0.9.5

Adds a compact Customer inbox component to the dashboard home so teams can see
actionable customer conversations without first opening the full CRM page.

## Changes

- Adds the suggested `customer-inbox` dashboard component at half or full width.
- Shows exact matching count, contact identity, channel, priority, automated
  flag, subject, latest-message preview and relative activity time.
- Supports per-widget default status (`open`, `pending`, or `all`), channel and
  a bounded 4–20 conversation limit.
- Refreshes when CRM message, status and activity events advance the dashboard
  revision, with a periodic fallback refresh and explicit retry state.
- Deep-links each row into the full CRM Inbox with the matching status and
  conversation selected.
- Keeps the home component read-only; replies and status changes remain in the
  full CRM panel to avoid accidental external sends.

## Compatibility

No database migration or API change is required. The component reuses CRM's
existing project-scoped `/inbox` endpoint and existing conversation events.
Installations that do not add the suggested component retain the current CRM
page unchanged.

## Validation

Tests cover widget preference bounds, scoped and filtered API URLs, deep-link
round-tripping, row fallbacks and relative timestamps. The UI build verifies
both CRM bundles are self-contained and importable against the dashboard React
surface, while the Go suite validates the published component manifest.
