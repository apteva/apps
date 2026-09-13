# Changelog

## 0.4.0 — 2026-09-13

Calendar events now remain correctly scoped to projects and recurring edits preserve series history. This release replaces the handwritten recurrence implementation, makes occurrence/split mutations transactional, validates input consistently, and fixes availability buffers and local-date handling.

- Correct monthly/leap-year recurrence, old ongoing series, COUNT-limited splits, future exception deletion, and retry/concurrent edits.
- Add IANA timezone recurrence, date-only all-day events, calendar moves, cancelled-event availability, and cancellable/bounded queries.
- Use indexed event-window queries and merged busy intervals. A local in-memory test with 50,000 historical events fell from about 89 ms to 0.13 ms per one-result query; production performance depends on data and hardware.
- Add occurrence scope, recurrence, timezone, status and all-day editor controls; overlap columns; cross-day drag previews; correct hour-label alignment; accessible dialogs and keyboard event actions; mobile navigation.
- Fix failed-delete feedback, stale loads, calendar/holiday refreshes, disabled-calendar recovery, month overflow and long-span year markers.
- Add regression, migration, isolation, cancellation and concurrency tests; browser coverage of recurring edits, calendar moves, drag previews, failure feedback and mobile creation; Calendar-specific CI.
- Upgrade the app SDK pin to v0.79.0 and use rrule-go v1.8.2. See README for migration and API-contract details.
