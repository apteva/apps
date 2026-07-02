-- Reusable customer payment methods and provider-hosted setup sessions.
--
-- Billing owns only the money primitive here: saved instruments,
-- provider references, and default selection. Product apps decide when
-- a customer needs a saved method and what to charge with it.

CREATE TABLE billing_payment_methods (
  id                         INTEGER PRIMARY KEY,
  project_id                 TEXT    NOT NULL,
  customer_id                INTEGER NOT NULL REFERENCES customers(id),

  provider                   TEXT    NOT NULL DEFAULT 'stripe',
  provider_customer_id       TEXT,
  provider_payment_method_id TEXT    NOT NULL,
  provider_mandate_id        TEXT,

  -- Stripe-style types: card, sepa_debit, us_bank_account, link, ...
  type                       TEXT    NOT NULL,
  status                     TEXT    NOT NULL DEFAULT 'active',
  is_default                 INTEGER NOT NULL DEFAULT 0,
  reusable                   INTEGER NOT NULL DEFAULT 1,
  delayed_notification       INTEGER NOT NULL DEFAULT 0,

  display_brand              TEXT,
  display_last4              TEXT,
  exp_month                  INTEGER,
  exp_year                   INTEGER,
  country                    TEXT,
  currency                   TEXT,

  metadata                   TEXT    NOT NULL DEFAULT '{}',

  created_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at                 TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  detached_at                TIMESTAMP
);

CREATE INDEX ix_bpm_customer
  ON billing_payment_methods(project_id, customer_id, status, updated_at DESC);

CREATE INDEX ix_bpm_provider_customer
  ON billing_payment_methods(provider, provider_customer_id);

CREATE UNIQUE INDEX ux_bpm_provider_pm_active
  ON billing_payment_methods(provider, provider_payment_method_id)
  WHERE detached_at IS NULL;

CREATE UNIQUE INDEX ux_bpm_customer_default
  ON billing_payment_methods(project_id, customer_id)
  WHERE is_default = 1 AND status = 'active';


CREATE TABLE billing_setup_sessions (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT    NOT NULL,
  customer_id              INTEGER NOT NULL REFERENCES customers(id),

  provider                 TEXT    NOT NULL DEFAULT 'stripe',
  provider_customer_id     TEXT,
  provider_session_id      TEXT    NOT NULL,
  provider_setup_intent_id TEXT,

  status                   TEXT    NOT NULL DEFAULT 'pending',
  success_url              TEXT,
  cancel_url               TEXT,
  url                      TEXT,
  payment_method_types     TEXT    NOT NULL DEFAULT '[]',
  metadata                 TEXT    NOT NULL DEFAULT '{}',

  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  completed_at             TIMESTAMP,
  expires_at               TIMESTAMP
);

CREATE INDEX ix_setup_sessions_customer
  ON billing_setup_sessions(project_id, customer_id, status, created_at DESC);

CREATE UNIQUE INDEX ux_setup_sessions_provider_session
  ON billing_setup_sessions(provider, provider_session_id);
