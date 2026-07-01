-- Hosting v1.9.0 — generic tenant add-ons and metered billing watermarks.

CREATE TABLE IF NOT EXISTS hosting_tenant_addons (
  id                    INTEGER PRIMARY KEY,
  tenant_id             TEXT    NOT NULL REFERENCES hosting_tenants(id) ON DELETE CASCADE,
  customer_id           INTEGER NOT NULL REFERENCES hosting_customers(id),
  addon_key             TEXT    NOT NULL,
  status                TEXT    NOT NULL DEFAULT 'active',
  feature_key           TEXT    NOT NULL,
  included_quantity     INTEGER NOT NULL DEFAULT 0,
  reset_interval        TEXT    NOT NULL DEFAULT 'month',
  catalog_product_id    INTEGER,
  catalog_price_id      INTEGER,
  overage_product_id    INTEGER,
  overage_price_id      INTEGER,
  subscription_id       INTEGER,
  subscription_item_id  INTEGER,
  unit_amount_cents     INTEGER NOT NULL DEFAULT 0,
  unit_size             INTEGER NOT NULL DEFAULT 1,
  currency              TEXT    NOT NULL DEFAULT 'USD',
  external_app          TEXT    NOT NULL DEFAULT '',
  external_subject_type TEXT    NOT NULL DEFAULT '',
  external_subject_id   TEXT    NOT NULL DEFAULT '',
  external_token_id     TEXT    NOT NULL DEFAULT '',
  metadata_json         TEXT    NOT NULL DEFAULT '{}',
  activated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  suspended_at          TIMESTAMP,
  cancelled_at          TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(tenant_id, addon_key)
);

CREATE INDEX IF NOT EXISTS ix_hosting_addons_tenant
  ON hosting_tenant_addons(tenant_id, status);
CREATE INDEX IF NOT EXISTS ix_hosting_addons_subscription
  ON hosting_tenant_addons(subscription_id, status)
  WHERE subscription_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_hosting_addons_feature
  ON hosting_tenant_addons(feature_key, status);

CREATE TABLE IF NOT EXISTS hosting_metering_periods (
  id                  INTEGER PRIMARY KEY,
  addon_id            INTEGER NOT NULL REFERENCES hosting_tenant_addons(id) ON DELETE CASCADE,
  tenant_id           TEXT    NOT NULL,
  customer_id         INTEGER NOT NULL,
  feature_key         TEXT    NOT NULL,
  period_start        TIMESTAMP NOT NULL,
  period_end          TIMESTAMP NOT NULL,
  included_quantity   INTEGER NOT NULL DEFAULT 0,
  total_quantity      INTEGER NOT NULL DEFAULT 0,
  billable_quantity   INTEGER NOT NULL DEFAULT 0,
  unit_amount_cents   INTEGER NOT NULL DEFAULT 0,
  unit_size           INTEGER NOT NULL DEFAULT 1,
  currency            TEXT    NOT NULL DEFAULT 'USD',
  invoice_id          INTEGER,
  status              TEXT    NOT NULL DEFAULT 'draft',
  idempotency_key     TEXT    NOT NULL,
  metadata_json       TEXT    NOT NULL DEFAULT '{}',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  billed_at           TIMESTAMP,
  UNIQUE(addon_id, feature_key, period_start, period_end)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_hosting_metering_idempotency
  ON hosting_metering_periods(idempotency_key);
CREATE INDEX IF NOT EXISTS ix_hosting_metering_period
  ON hosting_metering_periods(status, period_end);
