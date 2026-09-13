# Calendar

Calendar v0.4.0 provides project-scoped calendars, events, recurring series, availability search, and selected fixed-date holidays. It runs as an Apteva app sidecar and exposes MCP tools and a dashboard panel.

## Dates and recurrence

- Timed `start_at` and `end_at` accept RFC3339 timestamps (or date/time strings interpreted as UTC). Stored instants use whole-second UTC timestamps and must have a positive duration.
- `timezone` is an IANA zone such as `Europe/Madrid`. It defaults to `UTC`. Recurrence uses that zone's wall time across daylight-saving transitions. Existing timed events retain UTC recurrence unless explicitly edited to another timezone.
- All-day events use date-only inputs or UTC-midnight timestamps, with an **exclusive** end date. July 14 is `start_at=2026-07-14`, `end_at=2026-07-15`. The panel and availability search treat these as calendar dates, independent of the viewer's UTC offset.
- Supported recurrence fields: `FREQ=DAILY|WEEKLY|MONTHLY|YEARLY`, `INTERVAL`, `COUNT` or `UNTIL`, `BYDAY`, `BYMONTHDAY`, `BYMONTH`, `BYSETPOS`, and `WKST`. Invalid dates are skipped according to RFC 5545; January 31 does not drift to March 3. Unknown fields are rejected.
- Event dates must be within 1900–2200, with a maximum individual duration of 10 years. A list window may span at most 370 days and return at most 20,000 occurrences. Exceeding a limit returns an explicit error.

## Editing and deleting

`events_update` supports `this`, `this_and_following`, and `all`. The default is `this` for recurring masters and `all` for one-offs. Occurrence scopes require the original `occurrence_start_at` returned by `events_list`, even when the event is moved.

`events_delete` defaults to `all` for compatibility; pass an explicit occurrence scope to limit deletion. The editor always makes the scope explicit.

Single-occurrence retries update the same replacement row. Series splits preserve the remaining count unless the caller explicitly supplies a replacement rule. Exclusions and child identities are rebased by occurrence index when a series schedule changes; overridden event content and explicit times are preserved. Shortening/removing recurrence removes overrides outside the resulting series. Related changes commit in one transaction.

Updates distinguish omitted fields from empty values: `description: ""`, `location: ""`, and `rrule: ""` clear them. `calendar_id` moves an event or series to another calendar in the same project. `status` accepts `confirmed`, `tentative`, or `cancelled`.

## Availability

`events_find_slot` accepts a timezone for local working hours (default UTC, Monday–Friday 09:00–18:00). It accounts for buffers on adjacent events outside the requested window and skips cancelled events. Confirmed/tentative events, including all-day holidays, block availability. Empty explicit working hours produce no slots.

Duration is 1–1,440 minutes; buffers are 0–1,440 minutes; result limit is 1–100. Searches may span at most 366 days. Busy intervals are merged before searching; query execution and recurrence expansion honor cancellation.

`holidays_set` supports a selected fixed-date subset for FR, US, and GB. It does not provide a complete national, movable, or observed-holiday calendar. Countries have separate calendars and repeated loads are idempotent.

## Project isolation

Every operation requires a project context. MCP uses the SDK's dispatched project. HTTP uses the gateway-authorized `X-Apteva-Project-ID` or the project-bound installation, and rejects mismatched query selectors. The panel addresses its specific installation and includes its project selector. Every calendar/event ID lookup checks ownership.

## Upgrade from v0.3.x

The migration adds timezone metadata and query indexes, canonicalizes old timestamps and exception identities, normalizes legacy all-day boundaries, and keeps the latest duplicate replacement for each original occurrence. On mount, old rules containing both COUNT and UNTIL are converted while preserving their actual final occurrence.

Old global-install rows with an empty `project_id` cannot be assigned an owner safely from the stored data. They remain intact and inaccessible through project-scoped operations until an administrator assigns them to the intended project. Separate project-bound installations are unaffected. No external synchronization or production data migration is performed by publishing this release.

## Validation

Use Bun for JavaScript tooling. Disable the workspace overlay to test the released SDK pin:

```sh
GOWORK=off go test -race -tags integration ./...
GOWORK=off go vet ./...
bun install --frozen-lockfile
bun run typecheck
TZ=Europe/Madrid bun test ui
bun run ../../scripts/build-panels.ts --app calendar
bunx --no-install playwright install chromium
bun run test:browser
GOWORK=off go build .
```

Browser tests start their own in-memory calendar service on `127.0.0.1:5319`; they never access a deployed instance. CI also checks committed panel bundle consistency and called Go vulnerabilities.
