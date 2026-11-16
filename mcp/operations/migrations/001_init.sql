-- operations v0.1: generic operations core for any physical business.
--
-- The schema knows nothing about trades. Field services, salons,
-- restaurants and the rest are expressed as configuration (work types,
-- forms, rate items, modifiers, policies) installed from business packs.
-- Everything is scoped by project_id.

-- ─── Configuration ─────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS packs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    key           TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    version       TEXT    NOT NULL DEFAULT '',
    manifest_json TEXT    NOT NULL,
    installed_at  TEXT    NOT NULL,
    UNIQUE(project_id, key)
);

CREATE TABLE IF NOT EXISTS work_types (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id            TEXT    NOT NULL,
    pack_key              TEXT    NOT NULL DEFAULT '',
    key                   TEXT    NOT NULL,
    name                  TEXT    NOT NULL,
    mode                  TEXT    NOT NULL DEFAULT 'at_site'
                          CHECK(mode IN ('at_customer','at_site','fulfilment','production','internal')),
    phases_json           TEXT    NOT NULL,   -- ordered subset of intake,quote,schedule,execute,bill,closed
    statuses_json         TEXT    NOT NULL DEFAULT '[]', -- extra [{key,label,phase,hold?}]
    required_forms_json   TEXT    NOT NULL DEFAULT '{}', -- {"<phase entered>": ["form_key"]}
    intake_form_key       TEXT    NOT NULL DEFAULT '',
    default_duration_min  INTEGER NOT NULL DEFAULT 60,
    buffer_min            INTEGER NOT NULL DEFAULT 0,
    requirements_json     TEXT    NOT NULL DEFAULT '[]', -- [{kind,count,quals,zone,capacity_from}]
    priority_rules_json   TEXT    NOT NULL DEFAULT '[]', -- [{keywords:[...], priority}]
    sla_json              TEXT    NOT NULL DEFAULT '{}', -- {"p1": minutes to first slot}
    quote_policy_json     TEXT    NOT NULL DEFAULT '{}', -- {auto_approve_max_cents}
    playbook              TEXT    NOT NULL DEFAULT '',
    active                INTEGER NOT NULL DEFAULT 1,
    updated_at            TEXT    NOT NULL,
    UNIQUE(project_id, key)
);

CREATE TABLE IF NOT EXISTS forms (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    pack_key    TEXT    NOT NULL DEFAULT '',
    key         TEXT    NOT NULL,
    version     INTEGER NOT NULL DEFAULT 1,
    name        TEXT    NOT NULL,
    purpose     TEXT    NOT NULL DEFAULT 'checklist', -- intake|checklist|inspection|compliance|signoff
    schema_json TEXT    NOT NULL,                     -- {fields:[{key,label,type,required,options,min,max}]}
    updated_at  TEXT    NOT NULL,
    UNIQUE(project_id, key)
);

CREATE TABLE IF NOT EXISTS rate_items (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT    NOT NULL,
    pack_key          TEXT    NOT NULL DEFAULT '',
    code              TEXT    NOT NULL,
    name              TEXT    NOT NULL,
    catalog_price_id  INTEGER,
    pricing_json      TEXT    NOT NULL,   -- {mode,unit,unit_cents,min_charge_cents,tiers,formula}
    cost_cents        INTEGER NOT NULL DEFAULT 0, -- cost per unit (materials + labour estimate)
    duration_json     TEXT    NOT NULL DEFAULT '{}', -- {fixed_min} | {per_unit_min}
    requirements_json TEXT    NOT NULL DEFAULT '[]',
    materials_json    TEXT    NOT NULL DEFAULT '[]', -- [{sku, qty_per_unit}]
    active            INTEGER NOT NULL DEFAULT 1,
    updated_at        TEXT    NOT NULL,
    UNIQUE(project_id, code)
);

CREATE TABLE IF NOT EXISTS modifiers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    pack_key    TEXT    NOT NULL DEFAULT '',
    key         TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('surcharge','discount','fee')),
    when_json   TEXT    NOT NULL DEFAULT '{}', -- {priorities,weekdays,hour_from,hour_to,party_size_gte,flags}
    amount_json TEXT    NOT NULL,              -- {pct} | {cents}
    active      INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    UNIQUE(project_id, key)
);

CREATE TABLE IF NOT EXISTS policies (
    project_id TEXT NOT NULL,
    key        TEXT NOT NULL,
    value_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(project_id, key)
);

-- ─── Places, resources, objects ───────────────────────────────────

CREATE TABLE IF NOT EXISTS sites (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id     TEXT    NOT NULL,
    kind           TEXT    NOT NULL DEFAULT 'own' CHECK(kind IN ('own','customer')),
    name           TEXT    NOT NULL,
    crm_contact_id INTEGER,
    address        TEXT    NOT NULL DEFAULT '',
    lat            REAL,
    lng            REAL,
    timezone       TEXT    NOT NULL DEFAULT 'UTC',
    hours_json     TEXT    NOT NULL DEFAULT '{}', -- {"mon":[["09:00","18:00"]],...}
    access_json    TEXT    NOT NULL DEFAULT '{}',
    custom_json    TEXT    NOT NULL DEFAULT '{}',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sites_project ON sites(project_id, kind);

CREATE TABLE IF NOT EXISTS resources (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK(kind IN ('person','crew','space','equipment','vehicle')),
    name            TEXT    NOT NULL,
    site_id         INTEGER,
    zone            TEXT    NOT NULL DEFAULT '',
    capacity        INTEGER NOT NULL DEFAULT 1,  -- seats, crew size, concurrent jobs
    quals_json      TEXT    NOT NULL DEFAULT '[]',
    crm_contact_id  INTEGER,
    gigs_worker_id  INTEGER,
    home_lat        REAL,
    home_lng        REAL,
    cost_rate_cents INTEGER NOT NULL DEFAULT 0,  -- per hour
    needs_shift     INTEGER NOT NULL DEFAULT 0,  -- 1 = only available inside shift blocks
    active          INTEGER NOT NULL DEFAULT 1,
    custom_json     TEXT    NOT NULL DEFAULT '{}',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resources_project ON resources(project_id, kind, active);

CREATE TABLE IF NOT EXISTS resource_blocks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    resource_id INTEGER NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK(kind IN ('shift','off','blocked')),
    starts_at   TEXT    NOT NULL,
    ends_at     TEXT    NOT NULL,
    note        TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_blocks_resource ON resource_blocks(resource_id, starts_at);

CREATE TABLE IF NOT EXISTS objects (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT    NOT NULL,
    site_id          INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    kind             TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    attributes_json  TEXT    NOT NULL DEFAULT '{}',
    installed_on     TEXT    NOT NULL DEFAULT '',
    warranty_until   TEXT    NOT NULL DEFAULT '',
    last_serviced_at TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_objects_site ON objects(site_id);

-- ─── Work ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS work (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT    NOT NULL,
    number           TEXT    NOT NULL,
    work_type_id     INTEGER NOT NULL REFERENCES work_types(id),
    mode             TEXT    NOT NULL,
    phase            TEXT    NOT NULL,
    status           TEXT    NOT NULL,
    hold_from_status TEXT    NOT NULL DEFAULT '',
    priority         TEXT    NOT NULL DEFAULT 'p3',
    source           TEXT    NOT NULL DEFAULT '',
    crm_contact_id   INTEGER,
    customer_name    TEXT    NOT NULL DEFAULT '',
    customer_email   TEXT    NOT NULL DEFAULT '',
    customer_phone   TEXT    NOT NULL DEFAULT '',
    site_id          INTEGER REFERENCES sites(id),
    object_ids_json  TEXT    NOT NULL DEFAULT '[]',
    party_size       INTEGER NOT NULL DEFAULT 0,
    summary          TEXT    NOT NULL DEFAULT '',
    intake_json      TEXT    NOT NULL DEFAULT '{}',
    custom_json      TEXT    NOT NULL DEFAULT '{}',
    agreement_id     INTEGER,
    parent_id        INTEGER,
    duration_min     INTEGER NOT NULL DEFAULT 0,
    currency         TEXT    NOT NULL DEFAULT 'EUR',
    estimate_cents   INTEGER NOT NULL DEFAULT 0,
    quote_status     TEXT    NOT NULL DEFAULT '', -- ''|draft|needs_owner|sent|accepted|declined
    quote_accepted_at TEXT,
    invoice_ref      TEXT    NOT NULL DEFAULT '',
    invoiced_cents   INTEGER NOT NULL DEFAULT 0,
    paid_at          TEXT,
    sla_due_at       TEXT,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    closed_at        TEXT,
    UNIQUE(project_id, number)
);
CREATE INDEX IF NOT EXISTS idx_work_project_phase ON work(project_id, phase, priority);
CREATE INDEX IF NOT EXISTS idx_work_project_updated ON work(project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS work_lines (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT    NOT NULL,
    work_id          INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
    source           TEXT    NOT NULL CHECK(source IN ('estimate','actual')),
    rate_item_id     INTEGER,
    code             TEXT    NOT NULL DEFAULT '',
    kind             TEXT    NOT NULL DEFAULT 'item' CHECK(kind IN ('item','modifier','custom')),
    description      TEXT    NOT NULL,
    qty              REAL    NOT NULL DEFAULT 1,
    unit             TEXT    NOT NULL DEFAULT '',
    unit_price_cents INTEGER NOT NULL DEFAULT 0,
    total_cents      INTEGER NOT NULL DEFAULT 0,
    cost_cents       INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lines_work ON work_lines(work_id, source);

-- A slot is a scheduled block of time for a work item: a field visit,
-- a salon appointment, a table reservation, a prep run.
CREATE TABLE IF NOT EXISTS slots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    work_id      INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
    starts_at    TEXT    NOT NULL,
    ends_at      TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'booked'
                 CHECK(status IN ('booked','in_progress','done','missed','cancelled')),
    eta          TEXT,
    started_at   TEXT,
    completed_at TEXT,
    notes        TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_slots_project_time ON slots(project_id, starts_at);
CREATE INDEX IF NOT EXISTS idx_slots_work ON slots(work_id);

CREATE TABLE IF NOT EXISTS slot_resources (
    slot_id     INTEGER NOT NULL REFERENCES slots(id) ON DELETE CASCADE,
    resource_id INTEGER NOT NULL REFERENCES resources(id),
    role        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY(slot_id, resource_id)
);
CREATE INDEX IF NOT EXISTS idx_slot_resources_resource ON slot_resources(resource_id);

CREATE TABLE IF NOT EXISTS form_responses (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    form_key     TEXT    NOT NULL,
    form_version INTEGER NOT NULL,
    work_id      INTEGER REFERENCES work(id) ON DELETE CASCADE,
    slot_id      INTEGER,
    site_id      INTEGER,
    object_id    INTEGER,
    responder    TEXT    NOT NULL DEFAULT '',
    data_json    TEXT    NOT NULL,
    submitted_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_forms_work ON form_responses(work_id, form_key);
CREATE INDEX IF NOT EXISTS idx_forms_project ON form_responses(project_id, form_key, submitted_at DESC);

-- Append-only timeline of a work item; read by work_timeline.
CREATE TABLE IF NOT EXISTS work_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    work_id      INTEGER NOT NULL REFERENCES work(id) ON DELETE CASCADE,
    at           TEXT    NOT NULL,
    actor        TEXT    NOT NULL DEFAULT '',
    kind         TEXT    NOT NULL,
    from_status  TEXT    NOT NULL DEFAULT '',
    to_status    TEXT    NOT NULL DEFAULT '',
    payload_json TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_events_work ON work_events(work_id, id);

-- ─── Recurring work ───────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS agreements (
    id                         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id                 TEXT    NOT NULL,
    name                       TEXT    NOT NULL,
    work_type_id               INTEGER NOT NULL REFERENCES work_types(id),
    crm_contact_id             INTEGER,
    customer_name              TEXT    NOT NULL DEFAULT '',
    customer_email             TEXT    NOT NULL DEFAULT '',
    customer_phone             TEXT    NOT NULL DEFAULT '',
    site_id                    INTEGER REFERENCES sites(id),
    recurrence_json            TEXT    NOT NULL, -- {every,unit,weekdays,time}
    items_json                 TEXT    NOT NULL DEFAULT '[]', -- [{code,qty}]
    answers_json               TEXT    NOT NULL DEFAULT '{}',
    preferred_resource_ids_json TEXT   NOT NULL DEFAULT '[]',
    auto_book                  INTEGER NOT NULL DEFAULT 1,
    starts_on                  TEXT    NOT NULL,
    ends_on                    TEXT    NOT NULL DEFAULT '',
    next_on                    TEXT    NOT NULL,
    status                     TEXT    NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','ended')),
    created_at                 TEXT    NOT NULL,
    updated_at                 TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agreements_due ON agreements(project_id, status, next_on);
