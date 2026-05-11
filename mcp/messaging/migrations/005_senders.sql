-- v0.9.0: senders table.
--
-- Mirror of the provider's identity list (SES email/domain identities,
-- Twilio phone numbers / WhatsApp senders) plus local-only state
-- (per-project default sender, inbound bootstrap config, operator
-- notes, metadata for provider-specific compliance state).
--
-- Before this migration, senders_list / senders_get hit the provider
-- on every call — fine at low scale, slow at panel-mount cadence. Now
-- we keep a cached mirror that's reconciled via senders_refresh (and
-- a TTL on read).
--
-- Source-of-truth is still the provider for the actual send-gate —
-- if SES rejects a "from" we record the send failure, regardless of
-- what this table says.

CREATE TABLE senders (
  id                          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id                  TEXT NOT NULL,
  channel                     TEXT NOT NULL,         -- 'email' | 'sms' | 'whatsapp'
  address                     TEXT NOT NULL,         -- 'support@x.com' | 'x.com' | '+15551234567'
  kind                        TEXT NOT NULL,         -- 'email' | 'domain' | 'phone' | 'messaging_service' | 'sender_id'
  display_name                TEXT,

  -- Provider mirror.
  provider                    TEXT NOT NULL,         -- 'aws-ses' | 'twilio'
  provider_identity_id        TEXT,                  -- SES identity name | Twilio SID (PN…, MG…)
  verified                    INTEGER NOT NULL DEFAULT 0,
  verification_status         TEXT,                  -- 'pending' | 'verified' | 'failed' | 'inactive'
  sending_enabled             INTEGER NOT NULL DEFAULT 1,
  dkim_status                 TEXT,                  -- email-only: 'PENDING' | 'SUCCESS' | 'FAILED'

  -- Universal inbound flag + provider-specific config blob.
  inbound_bootstrapped        INTEGER NOT NULL DEFAULT 0,
  inbound_config              TEXT,                  -- JSON; SES: bucket/topic/subscription/region/rule_set/rule. Twilio: sms_url / previous_sms_url.

  -- Local-only state.
  is_default                  INTEGER NOT NULL DEFAULT 0,
  notes                       TEXT,                  -- operator-set free text
  metadata                    TEXT,                  -- JSON; provider-specific compliance state (A2P 10DLC brand/campaign ids, regulatory bundles, …)

  -- Sync metadata.
  last_synced_at              DATETIME,
  last_sync_error             TEXT,

  created_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at                  DATETIME,              -- soft delete

  UNIQUE (project_id, channel, address)
);

CREATE INDEX idx_senders_project_channel
  ON senders (project_id, channel)
  WHERE deleted_at IS NULL;

-- At most one default per (project, channel). Partial unique index
-- gives us the constraint at the SQL layer — no read-then-write race
-- between two writers can both win.
CREATE UNIQUE INDEX idx_senders_default
  ON senders (project_id, channel)
  WHERE is_default = 1 AND deleted_at IS NULL;
