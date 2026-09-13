ALTER TABLE events ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
-- Canonicalize old HTTP timestamps and exception identities before enforcing
-- uniqueness; .000Z and Z must identify the same occurrence.
UPDATE events SET start_at=strftime('%Y-%m-%dT%H:%M:%SZ',start_at) WHERE julianday(start_at) IS NOT NULL;
UPDATE events SET end_at=strftime('%Y-%m-%dT%H:%M:%SZ',end_at) WHERE julianday(end_at) IS NOT NULL;
UPDATE events SET occurrence_start_at=strftime('%Y-%m-%dT%H:%M:%SZ',occurrence_start_at)
 WHERE parent_event_id IS NOT NULL AND julianday(occurrence_start_at) IS NOT NULL;
UPDATE events SET exdate=(SELECT json_group_array(COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',value),value)) FROM json_each(events.exdate)) WHERE json_valid(exdate);
-- Old releases allowed retries to create duplicate overrides. Keep the latest
-- edit, preserving one replacement for each original occurrence.
DELETE FROM events WHERE parent_event_id IS NOT NULL AND id NOT IN (
 SELECT MAX(id) FROM events WHERE parent_event_id IS NOT NULL
 GROUP BY parent_event_id, occurrence_start_at
);
CREATE UNIQUE INDEX idx_events_occurrence ON events(parent_event_id, occurrence_start_at)
 WHERE parent_event_id IS NOT NULL;
CREATE INDEX idx_events_window_end ON events(calendar_id, end_at) WHERE rrule='';
ALTER TABLE calendars ADD COLUMN holiday_country TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_holiday_country ON calendars(project_id, holiday_country)
 WHERE holiday_country<>'';
-- Index recurring masters separately from non-recurring window queries.
CREATE INDEX idx_events_recurring ON events(calendar_id,start_at) WHERE rrule<>'';
-- Legacy all-day rows sometimes used timed boundaries. Retain their calendar
-- dates and round the exclusive end up when it included part of its last date.
UPDATE events SET end_at=CASE
 WHEN substr(end_at,1,10)<=substr(start_at,1,10) THEN date(substr(start_at,1,10),'+1 day')||'T00:00:00Z'
 WHEN substr(end_at,12,8)<>'00:00:00' THEN date(substr(end_at,1,10),'+1 day')||'T00:00:00Z'
 ELSE substr(end_at,1,10)||'T00:00:00Z' END,
 start_at=substr(start_at,1,10)||'T00:00:00Z'
WHERE all_day=1 AND julianday(start_at) IS NOT NULL AND julianday(end_at) IS NOT NULL;
