-- Partner Program v0.1.0
--
-- Merchant-side partner/referral/affiliate MVP. Kept deliberately to
-- five tables:
--   partners -> campaigns/referral_links -> referrals -> commissions.

CREATE TABLE IF NOT EXISTS partners (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  crm_contact_id INTEGER NOT NULL DEFAULT 0,
  name           TEXT NOT NULL,
  email          TEXT NOT NULL DEFAULT '',
  type           TEXT NOT NULL DEFAULT 'affiliate'
                 CHECK(type IN ('customer','affiliate','agency','reseller','internal')),
  status         TEXT NOT NULL DEFAULT 'pending'
                 CHECK(status IN ('pending','approved','rejected','suspended')),
  payout_email   TEXT NOT NULL DEFAULT '',
  metadata       TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_partners_project_status
  ON partners(project_id, status, type);

CREATE UNIQUE INDEX IF NOT EXISTS ux_partners_project_email
  ON partners(project_id, email)
  WHERE email <> '';


CREATE TABLE IF NOT EXISTS campaigns (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id               TEXT NOT NULL,
  name                     TEXT NOT NULL,
  slug                     TEXT NOT NULL,
  destination_url          TEXT NOT NULL DEFAULT '',
  status                   TEXT NOT NULL DEFAULT 'active'
                           CHECK(status IN ('draft','active','paused','archived')),
  default_commission_type  TEXT NOT NULL DEFAULT 'percent'
                           CHECK(default_commission_type IN ('none','fixed','percent')),
  default_commission_value REAL NOT NULL DEFAULT 20,
  cookie_days              INTEGER NOT NULL DEFAULT 60,
  metadata                 TEXT NOT NULL DEFAULT '{}',
  created_at               TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_campaigns_project_status
  ON campaigns(project_id, status);


CREATE TABLE IF NOT EXISTS referral_links (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT NOT NULL,
  partner_id      INTEGER NOT NULL REFERENCES partners(id) ON DELETE CASCADE,
  campaign_id     INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  code            TEXT NOT NULL,
  short_url       TEXT NOT NULL DEFAULT '',
  destination_url TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK(status IN ('active','paused','archived')),
  clicks          INTEGER NOT NULL DEFAULT 0,
  conversions     INTEGER NOT NULL DEFAULT 0,
  metadata        TEXT NOT NULL DEFAULT '{}',
  created_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, code)
);

CREATE INDEX IF NOT EXISTS idx_referral_links_lookup
  ON referral_links(project_id, partner_id, campaign_id, status);


CREATE TABLE IF NOT EXISTS referrals (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id               TEXT NOT NULL,
  partner_id               INTEGER NOT NULL REFERENCES partners(id) ON DELETE CASCADE,
  campaign_id              INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  referral_link_id         INTEGER REFERENCES referral_links(id) ON DELETE SET NULL,
  crm_contact_id           INTEGER NOT NULL DEFAULT 0,
  customer_email           TEXT NOT NULL DEFAULT '',
  external_customer_id     TEXT NOT NULL DEFAULT '',
  external_order_id        TEXT NOT NULL DEFAULT '',
  external_subscription_id TEXT NOT NULL DEFAULT '',
  status                   TEXT NOT NULL DEFAULT 'lead'
                           CHECK(status IN ('lead','converted','rejected','refunded','cancelled')),
  amount_cents             INTEGER NOT NULL DEFAULT 0,
  currency                 TEXT NOT NULL DEFAULT 'USD',
  source_event             TEXT NOT NULL DEFAULT '{}',
  metadata                 TEXT NOT NULL DEFAULT '{}',
  created_at               TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  converted_at             TEXT NOT NULL DEFAULT '',
  updated_at               TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_referrals_lookup
  ON referrals(project_id, partner_id, campaign_id, status, created_at);

CREATE INDEX IF NOT EXISTS idx_referrals_external_customer
  ON referrals(project_id, external_customer_id);


CREATE TABLE IF NOT EXISTS commissions (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id    TEXT NOT NULL,
  partner_id    INTEGER NOT NULL REFERENCES partners(id) ON DELETE CASCADE,
  referral_id   INTEGER NOT NULL REFERENCES referrals(id) ON DELETE CASCADE,
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK(status IN ('pending','approved','rejected','void','paid')),
  amount_cents  INTEGER NOT NULL DEFAULT 0,
  currency      TEXT NOT NULL DEFAULT 'USD',
  reason        TEXT NOT NULL DEFAULT '',
  eligible_at   TEXT NOT NULL DEFAULT '',
  approved_at   TEXT NOT NULL DEFAULT '',
  paid_at       TEXT NOT NULL DEFAULT '',
  payout_batch  TEXT NOT NULL DEFAULT '',
  metadata      TEXT NOT NULL DEFAULT '{}',
  created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commissions_lookup
  ON commissions(project_id, partner_id, status, eligible_at);
