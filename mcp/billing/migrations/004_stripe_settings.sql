-- Stripe direct-mode settings.
--
-- stripe_secret_key is app config and is not copied into the DB. This
-- table stores the webhook endpoint Billing creates in Stripe and the
-- returned signing secret needed to verify incoming Stripe webhooks.

CREATE TABLE billing_stripe_settings (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  webhook_endpoint_id TEXT    NOT NULL DEFAULT '',
  webhook_secret      TEXT    NOT NULL DEFAULT '',
  webhook_url         TEXT    NOT NULL DEFAULT '',
  mode                TEXT    NOT NULL DEFAULT 'direct',
  created_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
