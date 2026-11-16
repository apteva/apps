-- operations v0.2: brands.
--
-- A brand is what the customer sees: name, contact details, price
-- list, policies and job numbers. Resources stay shared across brands
-- so one scheduler sees all the work. brand_id 0 means shared (config)
-- or no brand (work), so a project without brands works as in v0.1.
--
-- The config tables are rebuilt to make their keys unique per brand
-- (two brands can both have a "repair" type or a "CALLOUT" price).
-- Row ids are kept, so work and agreements still point at the same
-- work types. Foreign keys are off during the rebuild and checked by
-- the migration runner before commit.

PRAGMA foreign_keys=OFF;

CREATE TABLE IF NOT EXISTS brands (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    key           TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    legal_name    TEXT    NOT NULL DEFAULT '',
    email         TEXT    NOT NULL DEFAULT '',
    phone         TEXT    NOT NULL DEFAULT '',
    website       TEXT    NOT NULL DEFAULT '',
    logo_file_id  INTEGER,
    number_prefix TEXT    NOT NULL,
    playbook      TEXT    NOT NULL DEFAULT '', -- how the agent speaks as this brand
    status        TEXT    NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    UNIQUE(project_id, key)
);

-- ─── packs ────────────────────────────────────────────────────────
CREATE TABLE packs_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    TEXT    NOT NULL,
    brand_id      INTEGER NOT NULL DEFAULT 0,
    key           TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    version       TEXT    NOT NULL DEFAULT '',
    manifest_json TEXT    NOT NULL,
    installed_at  TEXT    NOT NULL,
    UNIQUE(project_id, brand_id, key)
);
INSERT INTO packs_new(id,project_id,brand_id,key,name,version,manifest_json,installed_at)
    SELECT id,project_id,0,key,name,version,manifest_json,installed_at FROM packs;
DROP TABLE packs;
ALTER TABLE packs_new RENAME TO packs;

-- ─── work types ───────────────────────────────────────────────────
CREATE TABLE work_types_new (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id            TEXT    NOT NULL,
    brand_id              INTEGER NOT NULL DEFAULT 0,
    pack_key              TEXT    NOT NULL DEFAULT '',
    key                   TEXT    NOT NULL,
    name                  TEXT    NOT NULL,
    mode                  TEXT    NOT NULL DEFAULT 'at_site'
                          CHECK(mode IN ('at_customer','at_site','fulfilment','production','internal')),
    phases_json           TEXT    NOT NULL,
    statuses_json         TEXT    NOT NULL DEFAULT '[]',
    required_forms_json   TEXT    NOT NULL DEFAULT '{}',
    intake_form_key       TEXT    NOT NULL DEFAULT '',
    default_duration_min  INTEGER NOT NULL DEFAULT 60,
    buffer_min            INTEGER NOT NULL DEFAULT 0,
    requirements_json     TEXT    NOT NULL DEFAULT '[]',
    priority_rules_json   TEXT    NOT NULL DEFAULT '[]',
    sla_json              TEXT    NOT NULL DEFAULT '{}',
    quote_policy_json     TEXT    NOT NULL DEFAULT '{}',
    playbook              TEXT    NOT NULL DEFAULT '',
    active                INTEGER NOT NULL DEFAULT 1,
    updated_at            TEXT    NOT NULL,
    UNIQUE(project_id, brand_id, key)
);
INSERT INTO work_types_new(id,project_id,brand_id,pack_key,key,name,mode,phases_json,statuses_json,required_forms_json,intake_form_key,
        default_duration_min,buffer_min,requirements_json,priority_rules_json,sla_json,quote_policy_json,playbook,active,updated_at)
    SELECT id,project_id,0,pack_key,key,name,mode,phases_json,statuses_json,required_forms_json,intake_form_key,
        default_duration_min,buffer_min,requirements_json,priority_rules_json,sla_json,quote_policy_json,playbook,active,updated_at FROM work_types;
DROP TABLE work_types;
ALTER TABLE work_types_new RENAME TO work_types;

-- ─── forms ────────────────────────────────────────────────────────
CREATE TABLE forms_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    brand_id    INTEGER NOT NULL DEFAULT 0,
    pack_key    TEXT    NOT NULL DEFAULT '',
    key         TEXT    NOT NULL,
    version     INTEGER NOT NULL DEFAULT 1,
    name        TEXT    NOT NULL,
    purpose     TEXT    NOT NULL DEFAULT 'checklist',
    schema_json TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    UNIQUE(project_id, brand_id, key)
);
INSERT INTO forms_new(id,project_id,brand_id,pack_key,key,version,name,purpose,schema_json,updated_at)
    SELECT id,project_id,0,pack_key,key,version,name,purpose,schema_json,updated_at FROM forms;
DROP TABLE forms;
ALTER TABLE forms_new RENAME TO forms;

-- ─── rate items ───────────────────────────────────────────────────
CREATE TABLE rate_items_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        TEXT    NOT NULL,
    brand_id          INTEGER NOT NULL DEFAULT 0,
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
    UNIQUE(project_id, brand_id, code)
);
INSERT INTO rate_items_new(id,project_id,brand_id,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,
        requirements_json,materials_json,active,updated_at)
    SELECT id,project_id,0,pack_key,code,name,catalog_price_id,pricing_json,cost_cents,duration_json,
        requirements_json,materials_json,active,updated_at FROM rate_items;
DROP TABLE rate_items;
ALTER TABLE rate_items_new RENAME TO rate_items;

-- ─── modifiers ────────────────────────────────────────────────────
CREATE TABLE modifiers_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    brand_id    INTEGER NOT NULL DEFAULT 0,
    pack_key    TEXT    NOT NULL DEFAULT '',
    key         TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('surcharge','discount','fee')),
    when_json   TEXT    NOT NULL DEFAULT '{}',
    amount_json TEXT    NOT NULL,
    active      INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    UNIQUE(project_id, brand_id, key)
);
INSERT INTO modifiers_new(id,project_id,brand_id,pack_key,key,name,kind,when_json,amount_json,active,updated_at)
    SELECT id,project_id,0,pack_key,key,name,kind,when_json,amount_json,active,updated_at FROM modifiers;
DROP TABLE modifiers;
ALTER TABLE modifiers_new RENAME TO modifiers;

-- ─── policies: a brand's value overrides the project's ────────────
CREATE TABLE policies_new (
    project_id TEXT    NOT NULL,
    brand_id   INTEGER NOT NULL DEFAULT 0,
    key        TEXT    NOT NULL,
    value_json TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    PRIMARY KEY(project_id, brand_id, key)
);
INSERT INTO policies_new(project_id,brand_id,key,value_json,updated_at)
    SELECT project_id,0,key,value_json,updated_at FROM policies;
DROP TABLE policies;
ALTER TABLE policies_new RENAME TO policies;

-- ─── brand on records ─────────────────────────────────────────────
ALTER TABLE sites      ADD COLUMN brand_id INTEGER NOT NULL DEFAULT 0;      -- 0 = shared
ALTER TABLE resources  ADD COLUMN brand_ids_json TEXT NOT NULL DEFAULT '[]'; -- [] = works for every brand
ALTER TABLE work       ADD COLUMN brand_id INTEGER NOT NULL DEFAULT 0;      -- 0 = no brand
ALTER TABLE agreements ADD COLUMN brand_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_work_project_brand ON work(project_id, brand_id, phase);
