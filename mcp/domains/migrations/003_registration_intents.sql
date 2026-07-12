CREATE TABLE registration_intents (
  token           TEXT PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  domain          TEXT    NOT NULL,
  years           INTEGER NOT NULL,
  auto_renew      INTEGER NOT NULL,
  whois_privacy   INTEGER NOT NULL,
  coupon          TEXT    NOT NULL DEFAULT '',
  notes           TEXT    NOT NULL DEFAULT '',
  provider_slug   TEXT    NOT NULL,
  connection_id   INTEGER NOT NULL,
  price           TEXT    NOT NULL DEFAULT '',
  currency        TEXT    NOT NULL DEFAULT '',
  status          TEXT    NOT NULL DEFAULT 'prepared',
  response_json   TEXT    NOT NULL DEFAULT '',
  error_message   TEXT    NOT NULL DEFAULT '',
  expires_at      TEXT    NOT NULL,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_registration_intents_project_status
  ON registration_intents(project_id, status, expires_at);
