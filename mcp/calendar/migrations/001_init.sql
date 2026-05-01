-- calendar v0.1: multiple calendars + events with recurrence.
--
-- Recurrence is stored as an rrule string on the master row. The
-- handler expands occurrences at read-time inside events_list. Editing
-- a single occurrence creates a child row pointing at the master via
-- parent_event_id and adds the original date to the master's exdate
-- list; that way the master stays the source of truth for the rule
-- but the override has its own identity.
--
-- Times are stored as RFC3339 UTC. Per-user timezone is a v0.2 concern;
-- v0.1 honours APTEVA_TIMEZONE on the sidecar (defaults to UTC).

CREATE TABLE IF NOT EXISTS calendars (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    color       TEXT    NOT NULL DEFAULT '#3b82f6',  -- hex; UI maps to a chip
    kind        TEXT    NOT NULL DEFAULT 'custom'
                CHECK(kind IN ('personal','work','holidays','blocked','custom')),
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_calendars_project ON calendars(project_id, enabled);

CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id     INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    title           TEXT    NOT NULL,
    description     TEXT    NOT NULL DEFAULT '',
    location        TEXT    NOT NULL DEFAULT '',
    start_at        TEXT    NOT NULL,             -- RFC3339 UTC
    end_at          TEXT    NOT NULL,             -- RFC3339 UTC; same as start_at when all_day
    all_day         INTEGER NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL DEFAULT 'confirmed'
                    CHECK(status IN ('confirmed','tentative','cancelled')),
    rrule           TEXT    NOT NULL DEFAULT '',  -- empty = one-off
    exdate          TEXT    NOT NULL DEFAULT '[]',-- JSON array of RFC3339 UTC dates
    parent_event_id INTEGER REFERENCES events(id) ON DELETE CASCADE,
    -- override metadata: when parent_event_id is set, this row
    -- represents one materialised occurrence whose fields the user
    -- changed. occurrence_start_at is the original start of the
    -- occurrence being overridden (matches a date in the master's
    -- expansion); the master's exdate skips that date so we don't
    -- emit it twice.
    occurrence_start_at TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Window queries always filter by (calendar_id, start_at). Recurring
-- events with no upper bound are still emitted via expansion; the
-- index helps the master-row scan more than per-occurrence (since
-- occurrences aren't materialised).
CREATE INDEX IF NOT EXISTS idx_events_calendar_start ON events(calendar_id, start_at);
CREATE INDEX IF NOT EXISTS idx_events_parent ON events(parent_event_id);
