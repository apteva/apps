CREATE TABLE IF NOT EXISTS tax_profiles (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id           TEXT NOT NULL,
    name                 TEXT NOT NULL,
    country              TEXT NOT NULL,
    structure            TEXT NOT NULL,
    region               TEXT NOT NULL DEFAULT '',
    fiscal_year_start    TEXT NOT NULL DEFAULT '01-01',
    fiscal_year_end      TEXT NOT NULL DEFAULT '12-31',
    vat_registered       INTEGER NOT NULL DEFAULT 1,
    filing_cadence       TEXT NOT NULL DEFAULT 'quarterly',
    accounting_basis     TEXT NOT NULL DEFAULT 'accrual',
    currency             TEXT NOT NULL DEFAULT 'EUR',
    config_json          TEXT NOT NULL DEFAULT '{}',
    archived             INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_tax_profiles_scope ON tax_profiles(project_id, archived);

CREATE TABLE IF NOT EXISTS tax_rules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    country         TEXT NOT NULL,
    structure       TEXT NOT NULL,
    tax_type        TEXT NOT NULL,
    year            INTEGER NOT NULL,
    version         TEXT NOT NULL,
    effective_from  TEXT NOT NULL,
    effective_to    TEXT NOT NULL DEFAULT '',
    source_url      TEXT NOT NULL DEFAULT '',
    rules_json      TEXT NOT NULL DEFAULT '{}',
    active          INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(country, structure, tax_type, year, version)
);

CREATE INDEX IF NOT EXISTS idx_tax_rules_lookup ON tax_rules(country, structure, tax_type, year, active);

CREATE TABLE IF NOT EXISTS tax_periods (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    profile_id      INTEGER NOT NULL REFERENCES tax_profiles(id) ON DELETE CASCADE,
    tax_type        TEXT NOT NULL,
    period_start    TEXT NOT NULL,
    period_end      TEXT NOT NULL,
    due_date        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'open',
    filed_at        TEXT NOT NULL DEFAULT '',
    filing_ref      TEXT NOT NULL DEFAULT '',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, profile_id, tax_type, period_start, period_end)
);

CREATE INDEX IF NOT EXISTS idx_tax_periods_scope ON tax_periods(project_id, profile_id, tax_type, status);

CREATE TABLE IF NOT EXISTS tax_obligations (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT NOT NULL,
    profile_id        INTEGER NOT NULL REFERENCES tax_profiles(id) ON DELETE CASCADE,
    period_id         INTEGER REFERENCES tax_periods(id) ON DELETE SET NULL,
    calculation_id    INTEGER,
    tax_type          TEXT NOT NULL,
    authority         TEXT NOT NULL DEFAULT '',
    title             TEXT NOT NULL,
    amount_cents      INTEGER NOT NULL DEFAULT 0,
    currency          TEXT NOT NULL DEFAULT 'EUR',
    due_date          TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'estimated',
    filed_at          TEXT NOT NULL DEFAULT '',
    filing_ref        TEXT NOT NULL DEFAULT '',
    waived_reason     TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_obligations_scope ON tax_obligations(project_id, profile_id, tax_type, status, due_date);

CREATE TABLE IF NOT EXISTS tax_calculations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    profile_id      INTEGER NOT NULL REFERENCES tax_profiles(id) ON DELETE CASCADE,
    period_id       INTEGER REFERENCES tax_periods(id) ON DELETE SET NULL,
    tax_type        TEXT NOT NULL,
    rule_id         INTEGER REFERENCES tax_rules(id) ON DELETE SET NULL,
    rule_version    TEXT NOT NULL DEFAULT '',
    period_start    TEXT NOT NULL,
    period_end      TEXT NOT NULL,
    inputs_json     TEXT NOT NULL DEFAULT '{}',
    outputs_json    TEXT NOT NULL DEFAULT '{}',
    sources_json    TEXT NOT NULL DEFAULT '{}',
    warnings_json   TEXT NOT NULL DEFAULT '[]',
    confidence      TEXT NOT NULL DEFAULT 'medium',
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_calculations_scope ON tax_calculations(project_id, profile_id, tax_type, created_at);

CREATE TABLE IF NOT EXISTS tax_payments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT NOT NULL,
    obligation_id     INTEGER NOT NULL REFERENCES tax_obligations(id) ON DELETE CASCADE,
    amount_cents      INTEGER NOT NULL,
    currency          TEXT NOT NULL DEFAULT 'EUR',
    paid_at           TEXT NOT NULL,
    method            TEXT NOT NULL DEFAULT '',
    reference         TEXT NOT NULL DEFAULT '',
    bills_bill_id     INTEGER,
    bills_payment_id  INTEGER,
    notes             TEXT NOT NULL DEFAULT '',
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_payments_scope ON tax_payments(project_id, obligation_id, paid_at);

CREATE TABLE IF NOT EXISTS tax_adjustments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    profile_id      INTEGER NOT NULL REFERENCES tax_profiles(id) ON DELETE CASCADE,
    period_id       INTEGER REFERENCES tax_periods(id) ON DELETE SET NULL,
    tax_type        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    amount_cents    INTEGER NOT NULL,
    currency        TEXT NOT NULL DEFAULT 'EUR',
    reason          TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_adjustments_scope ON tax_adjustments(project_id, profile_id, tax_type, status);

CREATE TABLE IF NOT EXISTS tax_documents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    profile_id      INTEGER REFERENCES tax_profiles(id) ON DELETE SET NULL,
    period_id       INTEGER REFERENCES tax_periods(id) ON DELETE SET NULL,
    document_type   TEXT NOT NULL,
    title           TEXT NOT NULL,
    storage_file_id TEXT NOT NULL DEFAULT '',
    content_json    TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_documents_scope ON tax_documents(project_id, profile_id, period_id);

CREATE TABLE IF NOT EXISTS tax_audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT NOT NULL,
    entity_type   TEXT NOT NULL,
    entity_id     INTEGER NOT NULL,
    action        TEXT NOT NULL,
    message       TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tax_audit_scope ON tax_audit_log(project_id, entity_type, entity_id, created_at);

