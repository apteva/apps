-- Gigs v0.2 — commercial catalog, compensation, marketplace contracts.
--
-- Gigs owns operational scope and worker compensation. Catalog remains the
-- source of truth for customer-facing products/prices; Bills remains the
-- accounts-payable ledger. Every accepted commercial term is snapshotted so
-- later rate-card edits never rewrite historical work.

CREATE TABLE pay_grades (
  id                     INTEGER PRIMARY KEY,
  project_id             TEXT    NOT NULL,
  slug                   TEXT    NOT NULL,
  name                   TEXT    NOT NULL,
  rank                   INTEGER NOT NULL DEFAULT 0,
  description            TEXT,
  default_pricing_model  TEXT,
  default_amount_minor   INTEGER,
  currency               TEXT,
  active                 INTEGER NOT NULL DEFAULT 1,
  created_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK(default_pricing_model IS NULL OR default_pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(default_amount_minor IS NULL OR default_amount_minor >= 0)
);
CREATE UNIQUE INDEX ux_pay_grade_slug ON pay_grades(project_id, slug);
CREATE INDEX ix_pay_grades_active ON pay_grades(project_id, active, rank);

CREATE TABLE worker_pay_profiles (
  worker_id       INTEGER PRIMARY KEY REFERENCES workers(id) ON DELETE CASCADE,
  project_id      TEXT    NOT NULL,
  pay_grade_id    INTEGER NOT NULL REFERENCES pay_grades(id),
  currency        TEXT,
  metadata_json   TEXT,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_worker_pay_grade ON worker_pay_profiles(project_id, pay_grade_id);

CREATE TABLE standard_offers (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  template_id           INTEGER NOT NULL REFERENCES templates(id),
  slug                  TEXT    NOT NULL,
  name                  TEXT    NOT NULL,
  description           TEXT,
  category              TEXT,
  visibility            TEXT    NOT NULL DEFAULT 'private',
  status                TEXT    NOT NULL DEFAULT 'draft',
  version               INTEGER NOT NULL DEFAULT 1,
  catalog_product_id    INTEGER,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  published_at          TIMESTAMP,
  archived_at           TIMESTAMP,
  CHECK(visibility IN ('private','unlisted','public')),
  CHECK(status IN ('draft','active','archived'))
);
CREATE UNIQUE INDEX ux_standard_offer_slug ON standard_offers(project_id, slug);
CREATE INDEX ix_standard_offers_status ON standard_offers(project_id, status, category);

CREATE TABLE offer_packages (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  offer_id                 INTEGER NOT NULL REFERENCES standard_offers(id) ON DELETE CASCADE,
  slug                     TEXT    NOT NULL,
  name                     TEXT    NOT NULL,
  tier                     TEXT,
  description              TEXT,
  scope_json               TEXT,
  pricing_model            TEXT    NOT NULL,
  quantity                 REAL,
  unit                     TEXT,
  delivery_days            INTEGER,
  revisions                INTEGER,
  customer_amount_minor    INTEGER,
  currency                 TEXT,
  catalog_price_id         INTEGER,
  active                   INTEGER NOT NULL DEFAULT 1,
  sort_order               INTEGER NOT NULL DEFAULT 0,
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK(pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(quantity IS NULL OR quantity > 0),
  CHECK(customer_amount_minor IS NULL OR customer_amount_minor >= 0)
);
CREATE UNIQUE INDEX ux_offer_package_slug ON offer_packages(offer_id, slug);
CREATE INDEX ix_offer_packages_active ON offer_packages(project_id, offer_id, active, sort_order);

CREATE TABLE rate_cards (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  template_id           INTEGER REFERENCES templates(id),
  offer_package_id      INTEGER REFERENCES offer_packages(id),
  pay_grade_id          INTEGER REFERENCES pay_grades(id),
  worker_id             INTEGER REFERENCES workers(id),
  pricing_model         TEXT    NOT NULL,
  amount_minor          INTEGER NOT NULL,
  currency              TEXT    NOT NULL,
  unit                  TEXT,
  minimum_quantity      REAL,
  maximum_quantity      REAL,
  effective_from        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  effective_until       TIMESTAMP,
  status                TEXT    NOT NULL DEFAULT 'active',
  source                TEXT    NOT NULL DEFAULT 'configured',
  notes                 TEXT,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK(pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(amount_minor >= 0),
  CHECK(status IN ('draft','active','archived')),
  CHECK(worker_id IS NOT NULL OR pay_grade_id IS NOT NULL)
);
CREATE INDEX ix_rate_cards_resolve ON rate_cards(project_id, status, worker_id, pay_grade_id, template_id, offer_package_id);

CREATE TABLE job_posts (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  template_id           INTEGER REFERENCES templates(id),
  customer_contact_id   INTEGER,
  title                 TEXT    NOT NULL,
  description           TEXT,
  scope_json            TEXT,
  pricing_models_json   TEXT,
  budget_min_minor      INTEGER,
  budget_max_minor      INTEGER,
  currency              TEXT,
  deadline_at           TIMESTAMP,
  visibility            TEXT    NOT NULL DEFAULT 'private',
  status                TEXT    NOT NULL DEFAULT 'draft',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  published_at          TIMESTAMP,
  closed_at             TIMESTAMP,
  CHECK(visibility IN ('private','unlisted','public')),
  CHECK(status IN ('draft','open','awarded','closed','cancelled'))
);
CREATE INDEX ix_job_posts_status ON job_posts(project_id, status, created_at DESC);

CREATE TABLE proposals (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  job_post_id           INTEGER NOT NULL REFERENCES job_posts(id) ON DELETE CASCADE,
  worker_id             INTEGER NOT NULL REFERENCES workers(id),
  offer_package_id      INTEGER REFERENCES offer_packages(id),
  pricing_model         TEXT    NOT NULL,
  amount_minor          INTEGER NOT NULL,
  currency              TEXT    NOT NULL,
  estimated_days        INTEGER,
  message               TEXT,
  milestones_json       TEXT,
  status                TEXT    NOT NULL DEFAULT 'submitted',
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  accepted_at           TIMESTAMP,
  CHECK(pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(amount_minor >= 0),
  CHECK(status IN ('draft','submitted','accepted','rejected','withdrawn'))
);
CREATE UNIQUE INDEX ux_proposal_worker ON proposals(job_post_id, worker_id);
CREATE INDEX ix_proposals_status ON proposals(project_id, job_post_id, status);

CREATE TABLE contracts (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  source_type              TEXT    NOT NULL,
  source_id                INTEGER,
  customer_contact_id      INTEGER,
  worker_id                INTEGER REFERENCES workers(id),
  template_id              INTEGER REFERENCES templates(id),
  offer_id                 INTEGER REFERENCES standard_offers(id),
  offer_package_id         INTEGER REFERENCES offer_packages(id),
  title                    TEXT    NOT NULL,
  scope_json               TEXT,
  pricing_model            TEXT    NOT NULL,
  customer_amount_minor    INTEGER,
  worker_amount_minor      INTEGER,
  currency                 TEXT    NOT NULL,
  quantity                 REAL,
  unit                     TEXT,
  revision_limit           INTEGER,
  rate_source              TEXT,
  rate_card_id             INTEGER REFERENCES rate_cards(id),
  pay_grade_id             INTEGER REFERENCES pay_grades(id),
  status                   TEXT    NOT NULL DEFAULT 'draft',
  billing_invoice_id       INTEGER,
  order_id                 INTEGER,
  terms_json               TEXT,
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  accepted_at              TIMESTAMP,
  completed_at             TIMESTAMP,
  cancelled_at             TIMESTAMP,
  CHECK(source_type IN ('direct','package','proposal','order','subscription')),
  CHECK(pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(status IN ('draft','offered','active','completed','cancelled','disputed'))
);
CREATE INDEX ix_contracts_status ON contracts(project_id, status, created_at DESC);
CREATE INDEX ix_contracts_worker ON contracts(project_id, worker_id, status);

CREATE TABLE contract_milestones (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  contract_id              INTEGER NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
  title                    TEXT    NOT NULL,
  description              TEXT,
  sort_order               INTEGER NOT NULL DEFAULT 0,
  due_at                   TIMESTAMP,
  customer_amount_minor    INTEGER,
  worker_amount_minor      INTEGER,
  currency                 TEXT    NOT NULL,
  status                   TEXT    NOT NULL DEFAULT 'pending',
  gig_id                   INTEGER REFERENCES gigs(id),
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  accepted_at              TIMESTAMP,
  CHECK(status IN ('pending','active','submitted','approved','rejected','cancelled'))
);
CREATE INDEX ix_contract_milestones ON contract_milestones(contract_id, sort_order);

CREATE TABLE gig_compensation (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  gig_id                   INTEGER NOT NULL REFERENCES gigs(id) ON DELETE CASCADE,
  contract_id              INTEGER REFERENCES contracts(id),
  milestone_id             INTEGER REFERENCES contract_milestones(id),
  worker_id                INTEGER REFERENCES workers(id),
  pay_grade_id             INTEGER REFERENCES pay_grades(id),
  rate_card_id             INTEGER REFERENCES rate_cards(id),
  pricing_model            TEXT    NOT NULL,
  rate_amount_minor        INTEGER NOT NULL,
  quantity                 REAL    NOT NULL DEFAULT 1,
  unit                     TEXT,
  worker_amount_minor      INTEGER NOT NULL,
  customer_amount_minor    INTEGER,
  currency                 TEXT    NOT NULL,
  rate_source              TEXT    NOT NULL,
  override_reason          TEXT,
  agreed_at                TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  payable_status           TEXT    NOT NULL DEFAULT 'not_created',
  payable_bill_id          INTEGER,
  payable_error            TEXT,
  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CHECK(pricing_model IN ('fixed','hourly','per_unit','daily','milestone','recurring')),
  CHECK(rate_amount_minor >= 0),
  CHECK(quantity > 0),
  CHECK(worker_amount_minor >= 0),
  CHECK(payable_status IN ('not_created','pending','created','failed','not_applicable'))
);
CREATE UNIQUE INDEX ux_gig_compensation ON gig_compensation(gig_id);
CREATE INDEX ix_gig_compensation_payable ON gig_compensation(project_id, payable_status, agreed_at);
