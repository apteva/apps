# Events

Events manages small shows: venues, performer applications and reviews,
lineups, free or manually recorded tickets, and door check-in. Data is local
to the project's SQLite database. Meetup and Eventbrite are not connected
as backends.

## Version 0.2.0

- Create and edit event titles, descriptions, dates, timezone, venue, capacity,
  status, visibility, and external checkout URL in the dashboard panel.
- Public events have a responsive attendee page with free registration,
  ticket codes, performer applications, venue information, and lineup.
- Public HTML and JSON expose explicit public fields. Operational schedule
  notes, application references, project IDs, and internal counts are omitted.
- Ticket issuance verifies event ownership of the ticket type, active status,
  sales windows, and capacity. A type must be selected when the event has types.
- Capacity checks and inserts share a SQLite write transaction. Simultaneous
  requests cannot oversell either event or ticket-type inventory. Failed
  batches roll back both orders and tickets.
- Public registration issues free tickets only. An external checkout URL
  disables local public registration and provides a checkout link instead.
  Paid types cannot be issued through the public endpoint, including by
  omitting the type or supplying a manual source.
- Manual/agent issuance can record paid tickets, but does not collect payment
  or verify external purchases. External checkout does not automatically sync
  orders or attendees back to Events.
- Check-in rejects inactive tickets and preserves the original timestamp on
  repeated check-in requests.

## HTTP API and compatibility

Event CRUD uses `GET/POST /shows` and `GET/PATCH/PUT /shows/{id}`. This replaces
the old `/events` CRUD path, which collided with the SDK's reserved platform
event-ingestion endpoint and prevented startup. MCP tool names are unchanged.

Other administrative endpoints remain `/venues`, `/ticket_types`, `/tickets`,
`/tickets/{id}/check_in`, `/applications`, `/applications/{id}`, and `/slots`.
Administrative requests require the normal platform authentication.

Published public events are served at:

```text
/api/apps/events/public/{slug}?project_id={project_id}
```

Only `/public/` is marked anonymous in the runtime and install manifests.
The panel includes the project selector in both API requests and public links.

Public event GET requests return HTML by default. Request
`Accept: application/json` or add `format=json` for the sanitized JSON view.
The public JSON schema intentionally differs from the old storage-row response.

`POST /public/{slug}/register` accepts `buyer_name`, `buyer_email`, optional
`attendee_name`, `attendee_email`, `quantity` (up to 25), and `ticket_type_id`.
It returns a confirmation `message` and `ticket_codes`. Save those codes for
check-in; the app does not send ticket emails.

`POST /public/{slug}/apply` accepts `applicant_name`, `email`, and optional
performer fields, and returns `status` and a confirmation `message`.

Dates supplied by API clients can be RFC3339 timestamps with offsets.
The editor's local date/time values are interpreted in the selected IANA
timezone and normalized to UTC. Invalid dates, timezones, negative capacities,
non-HTTP checkout URLs, and end times before start times are rejected.
An ambiguous repeated time at the end of daylight saving can be specified
precisely through the API with an explicit RFC3339 offset.

## Validation

```sh
GOWORK=off go test -race ./...
GOWORK=off go build .
```

Tests cover public payment/type bypasses, sales windows, transaction rollback,
concurrent capacity limits with multiple database connections, safe public
responses, event editing and timezone handling, repeat/inactive check-in,
manifest consistency, and coexistence with the SDK's reserved routes.

To run against a disposable local database:

```sh
DB_PATH=/tmp/events-preview.db APTEVA_APP_PORT=18197 \
  APTEVA_PROJECT_ID=preview ./events
```

## Version 0.3.0 — artist workflow APIs

Version 0.3.0 adds migration `002_artist_workflow.sql` and self-contained native binaries with embedded UI/migrations. Source delivery is pinned to `events/v0.3.0`; app-sdk is pinned to v0.76.0. These workflow APIs power custom owner websites such as On Tap Comedy.

New HTTP operations (authenticated unless under `/public/`):

- `GET /public/`: upcoming published/closed public shows; cancelled links remain readable.
- `GET /shows/{id}/settings`, `PATCH /shows/{id}/settings`: application open flag, opening/deadline times (RFC3339), performer capacity, set minutes, performer instructions, email requirement, lineup publication, cancellation and cancellation reason.
- `POST /shows/{id}/duplicate`: copy settings and event details into a new private draft, clearing dates, application windows and publication.
- `GET /shows/{id}/export`: protected CSV of applications with spreadsheet formula escaping.
- `GET /applications/{id}/photo?download=1`: protected photo download.
- `PATCH /slots/{id}`, `DELETE /slots/{id}`: edit or remove a performance slot.
- `POST /public/{slug}/apply`: accepts a name and at least one contact method (email, phone, Instagram), plus optional stage name, bio, technical notes, and consented JPEG/PNG photo as base64. Email is configurable per event.
- `GET /public/{slug}/photos/{application_id}`: only consented photos from accepted applications with active slots on a published lineup.

Applications use transactional per-event contact identity deduplication and receive a generic acknowledgement. Performer capacity limits the lineup, not application intake. Applications may stay open for waitlist consideration. Scheduling checks capacity, event ownership, duplicate slots and overlap in the same transaction as insertion and acceptance. Changing a decision away from accepted cancels that artist's active slots. Slugs are permanent after creation.

Photos use private, persistent SQLite storage (2 MB maximum per image); include the Events database in backups. Photo delivery still checks consent, acceptance, active scheduling and lineup publication. External Storage integration and website authentication are separate concerns; the On Tap website uses Auth for owner access and these Events routes for event data.


### Atomic editing and schedule changes

`POST /shows` and `PATCH /shows/{id}` accept the normal event fields plus an optional `settings` object. Event details and settings commit together; failed validation leaves the existing event, settings and lineup unchanged and never creates a partial draft. `PATCH /shows/{id}/settings` remains supported.

Changing a show's start moves all scheduled, timed artist slots by the same UTC duration, preserving their offsets and lengths. Completed/cancelled slots stay unchanged. Shortening an event cannot leave slots outside its bounds, and a dated lineup prevents clearing the event date. Existing application deadlines remain explicit dates and must be adjusted when needed. Performer capacity cannot be lowered below the scheduled lineup count.

### Upgrade from 0.2.0

Back up the Events database before upgrading. Migration 002 is additive: it creates event-settings, application-identity and photo tables, plus an index. Existing events, venues, applications, slots, tickets and orders remain in place. Existing records do **not** receive retroactive contact-identity deduplication entries; historical duplicates require review rather than automatic merging.

Artist applications default to **closed** and public lineups default to **private**, including for existing events until their settings are saved explicitly. Set `applications_open: true` to accept performers, and `lineup_published: true` when the owner is ready to show the lineup. The generic dashboard retains its existing event/ticket tools; the new settings, private-photo and lineup-publication controls are provided by these HTTP APIs for owner frontends. This release does not install or deploy an owner website.

Slugs are immutable after creation. Existing integrations that rename slugs must create a new event or duplicate it. Public `/apply` now accepts a name plus at least one of email, phone or Instagram; an event can additionally require email. Public responses omit private contact details and review notes. Meetup/Eventbrite syncing, paid checkout processing and email sending remain outside this release.

The Events CI workflow runs the race suite, builds standalone Linux and Darwin binaries, and rehearses migration from a real v0.2.0 database using `scripts/check-upgrade.py`. The migration check starts the new binary from an empty working directory to verify embedded assets.
