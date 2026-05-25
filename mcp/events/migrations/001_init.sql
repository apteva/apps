-- events v0.1: lightweight event, ticket, performer application, and
-- schedule management. Times are stored as RFC3339 UTC strings.

CREATE TABLE IF NOT EXISTS venues (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    address     TEXT    NOT NULL DEFAULT '',
    city        TEXT    NOT NULL DEFAULT '',
    country     TEXT    NOT NULL DEFAULT '',
    capacity    INTEGER NOT NULL DEFAULT 0,
    notes       TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_venues_scope ON venues(project_id, name);

CREATE TABLE IF NOT EXISTS events (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id            TEXT    NOT NULL,
    title                 TEXT    NOT NULL,
    slug                  TEXT    NOT NULL,
    description           TEXT    NOT NULL DEFAULT '',
    status                TEXT    NOT NULL DEFAULT 'draft'
                          CHECK(status IN ('draft','published','closed','archived')),
    visibility            TEXT    NOT NULL DEFAULT 'private'
                          CHECK(visibility IN ('private','public')),
    timezone              TEXT    NOT NULL DEFAULT 'UTC',
    starts_at             TEXT    NOT NULL DEFAULT '',
    ends_at               TEXT    NOT NULL DEFAULT '',
    venue_id              INTEGER REFERENCES venues(id) ON DELETE SET NULL,
    capacity              INTEGER NOT NULL DEFAULT 0,
    external_checkout_url TEXT    NOT NULL DEFAULT '',
    created_at            TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_events_scope_status ON events(project_id, status, starts_at);

CREATE TABLE IF NOT EXISTS ticket_types (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name           TEXT    NOT NULL,
    description    TEXT    NOT NULL DEFAULT '',
    price_cents    INTEGER NOT NULL DEFAULT 0,
    currency       TEXT    NOT NULL DEFAULT 'USD',
    capacity       INTEGER NOT NULL DEFAULT 0,
    sales_start_at TEXT    NOT NULL DEFAULT '',
    sales_end_at   TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT 'active'
                   CHECK(status IN ('active','paused','sold_out','archived')),
    created_at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_ticket_types_event ON ticket_types(event_id, status);

CREATE TABLE IF NOT EXISTS orders (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    buyer_name  TEXT    NOT NULL,
    buyer_email TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'confirmed'
                CHECK(status IN ('pending','confirmed','cancelled','refunded')),
    total_cents INTEGER NOT NULL DEFAULT 0,
    currency    TEXT    NOT NULL DEFAULT 'USD',
    source      TEXT    NOT NULL DEFAULT 'manual'
                CHECK(source IN ('manual','public','agent')),
    created_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_orders_event ON orders(event_id, status);

CREATE TABLE IF NOT EXISTS tickets (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id       INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    event_id       INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    ticket_type_id INTEGER REFERENCES ticket_types(id) ON DELETE SET NULL,
    attendee_name  TEXT    NOT NULL,
    attendee_email TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT 'active'
                   CHECK(status IN ('active','voided','refunded')),
    checkin_status TEXT    NOT NULL DEFAULT 'not_checked_in'
                   CHECK(checkin_status IN ('not_checked_in','checked_in')),
    checked_in_at  TEXT    NOT NULL DEFAULT '',
    code           TEXT    NOT NULL UNIQUE,
    created_at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tickets_event ON tickets(event_id, status, checkin_status);
CREATE INDEX IF NOT EXISTS idx_tickets_code ON tickets(code);

CREATE TABLE IF NOT EXISTS performer_applications (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id           INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    applicant_name     TEXT    NOT NULL,
    stage_name         TEXT    NOT NULL DEFAULT '',
    email              TEXT    NOT NULL,
    phone              TEXT    NOT NULL DEFAULT '',
    bio                TEXT    NOT NULL DEFAULT '',
    set_length_minutes INTEGER NOT NULL DEFAULT 0,
    video_url          TEXT    NOT NULL DEFAULT '',
    social_links_json  TEXT    NOT NULL DEFAULT '{}',
    availability_json  TEXT    NOT NULL DEFAULT '{}',
    tech_needs         TEXT    NOT NULL DEFAULT '',
    notes              TEXT    NOT NULL DEFAULT '',
    status             TEXT    NOT NULL DEFAULT 'submitted'
                       CHECK(status IN ('submitted','shortlisted','accepted','rejected','withdrawn')),
    score              INTEGER NOT NULL DEFAULT 0,
    reviewer_notes     TEXT    NOT NULL DEFAULT '',
    submitted_at       TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at         TEXT    NOT NULL DEFAULT '',
    created_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_applications_event_status ON performer_applications(event_id, status);

CREATE TABLE IF NOT EXISTS performance_slots (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    venue_id        INTEGER REFERENCES venues(id) ON DELETE SET NULL,
    application_id  INTEGER REFERENCES performer_applications(id) ON DELETE SET NULL,
    performer_name  TEXT    NOT NULL,
    title           TEXT    NOT NULL DEFAULT '',
    starts_at       TEXT    NOT NULL DEFAULT '',
    ends_at         TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'scheduled'
                    CHECK(status IN ('scheduled','cancelled','completed')),
    notes           TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_slots_event_time ON performance_slots(event_id, starts_at);
