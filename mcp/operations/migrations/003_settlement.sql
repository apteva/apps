PRAGMA foreign_keys=OFF;

-- Settlement: how the money flows when another company or person does
-- the work. resale (the default, as before): the brand invoices the
-- client and pays the contractor. commission: the contractor invoices
-- the client and the brand invoices the contractor a commission.

-- Terms on the resource; settlement_by_brand_json maps brand_id to
-- terms that replace the default for that brand.
ALTER TABLE resources ADD COLUMN settlement_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE resources ADD COLUMN settlement_by_brand_json TEXT NOT NULL DEFAULT '{}';

-- The contractor doing the job and a copy of its terms, taken when the
-- contractor is assigned or booked and frozen once the job is done.
ALTER TABLE work ADD COLUMN partner_resource_id INTEGER;
ALTER TABLE work ADD COLUMN settlement_mode TEXT NOT NULL DEFAULT 'resale';
ALTER TABLE work ADD COLUMN settlement_json TEXT NOT NULL DEFAULT '{}';
-- The contractor's own invoice to the client (commission mode).
ALTER TABLE work ADD COLUMN partner_invoice_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_work_project_partner ON work(project_id, partner_resource_id);

-- Rate items can belong to one resource: that resource's price (and
-- cost) for the item. Rebuilt to widen the unique key; ids are kept.
CREATE TABLE rate_items_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT    NOT NULL,
    brand_id          INTEGER NOT NULL DEFAULT 0,
    resource_id       INTEGER NOT NULL DEFAULT 0,
    pack_key          TEXT    NOT NULL DEFAULT '',
    code              TEXT    NOT NULL,
    name              TEXT    NOT NULL,
    catalog_price_id  INTEGER,
    pricing_json      TEXT    NOT NULL,
    cost_cents        INTEGER NOT NULL DEFAULT 0,
    duration_json     TEXT    NOT NULL DEFAULT '{}',
    requirements_json TEXT    NOT NULL DEFAULT '[]',
    materials_json    TEXT    NOT NULL DEFAULT '[]',
    active            INTEGER NOT NULL DEFAULT 1,
    updated_at        TEXT    NOT NULL,
    UNIQUE(project_id, brand_id, resource_id, code)
);
INSERT INTO rate_items_new(id,project_id,brand_id,resource_id,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,
        requirements_json,materials_json,active,updated_at)
    SELECT id,project_id,brand_id,0,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,
        requirements_json,materials_json,active,updated_at FROM rate_items;
DROP TABLE rate_items;
ALTER TABLE rate_items_new RENAME TO rate_items;

-- Commission owed by a contractor, one or more lines per job.
-- kind commission is the job's line; adjustment corrects a line that
-- was already invoiced (it can be negative). status: accrued →
-- invoiced → paid, or void.
CREATE TABLE IF NOT EXISTS commission_lines (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       TEXT    NOT NULL,
    work_id          INTEGER NOT NULL REFERENCES work(id),
    brand_id         INTEGER NOT NULL DEFAULT 0,
    resource_id      INTEGER NOT NULL,
    kind             TEXT    NOT NULL DEFAULT 'commission',
    status           TEXT    NOT NULL DEFAULT 'accrued',
    base_cents       INTEGER NOT NULL DEFAULT 0,
    base_source      TEXT    NOT NULL DEFAULT '',
    rate_pct         REAL    NOT NULL DEFAULT 0,
    fixed_cents      INTEGER NOT NULL DEFAULT 0,
    amount_cents     INTEGER NOT NULL DEFAULT 0,
    currency         TEXT    NOT NULL DEFAULT '',
    cadence          TEXT    NOT NULL DEFAULT 'per_job',
    invoice_ref      TEXT    NOT NULL DEFAULT '',
    reason           TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    invoiced_at      TEXT,
    paid_at          TEXT,
    voided_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_commissions_work ON commission_lines(work_id);
CREATE INDEX IF NOT EXISTS idx_commissions_partner ON commission_lines(project_id, resource_id, status);
CREATE INDEX IF NOT EXISTS idx_commissions_invoice ON commission_lines(project_id, invoice_ref);
