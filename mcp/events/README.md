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
