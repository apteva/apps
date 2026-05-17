-- v0.12.0: split anchor identities out of senders.
--
-- The `senders` table conflated two concepts: sendable addresses
-- (mailboxes, phones) and authentication anchors (DKIM-verified
-- domains, future WhatsApp Business Accounts). Anchors aren't valid
-- From values, but they were leaking into From dropdowns through
-- senders_list. They also have a distinct lifecycle (verify-once vs.
-- per-message use) and distinct fields (DKIM tokens / inbound
-- bootstrap state vs. sending_enabled / is_default).
--
-- Split:
--   senders     → kind in (email_mailbox, phone, whatsapp_number,
--                          twilio_messaging_service). is_sendable is
--                 implicit — this table is the sendable set.
--                 New parent_identity_id FK for inheritance edges
--                 (mailbox→domain, future whatsapp_number→WABA).
--   identities  → kind in (email_domain, whatsapp_business_account).
--                 Authentication anchors. Verify once; never appear
--                 in From dropdowns.
--
-- All existing data is preserved: kind='domain' rows move to
-- identities; kind='email' rows are renamed to 'email_mailbox' and
-- gain their parent_identity_id FK backfilled by suffix match.

CREATE TABLE identities (
  id                          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id                  TEXT NOT NULL,
  kind                        TEXT NOT NULL,         -- 'email_domain' | 'whatsapp_business_account'
  address                     TEXT NOT NULL,         -- domain name, WABA id, etc.

  provider                    TEXT NOT NULL,         -- 'aws-ses' | 'twilio' | …
  provider_identity_id        TEXT,                  -- SES identity name | WABA SID
  verified                    INTEGER NOT NULL DEFAULT 0,
  verification_status         TEXT,                  -- 'pending' | 'verified' | 'failed' | 'inactive'
  dkim_status                 TEXT,                  -- email-only: 'PENDING' | 'SUCCESS' | 'FAILED'

  -- Inbound bootstrap belongs here (per-domain config, not per-mailbox).
  inbound_bootstrapped        INTEGER NOT NULL DEFAULT 0,
  inbound_config              TEXT,                  -- JSON; SES: bucket/topic/subscription/region/rule_set/rule.

  notes                       TEXT,
  metadata                    TEXT,
  last_synced_at              DATETIME,
  last_sync_error             TEXT,

  created_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  deleted_at                  DATETIME,

  UNIQUE (project_id, kind, address)
);

CREATE INDEX idx_identities_project_kind
  ON identities (project_id, kind)
  WHERE deleted_at IS NULL;

-- Move existing kind='domain' rows from senders into identities. The
-- domain rows carry per-domain state (DKIM, inbound bootstrap config)
-- that has no analog on the sendable side; preserve everything.
INSERT INTO identities
  (project_id, kind, address, provider, provider_identity_id,
   verified, verification_status, dkim_status,
   inbound_bootstrapped, inbound_config,
   notes, metadata, last_synced_at, last_sync_error,
   created_at, updated_at, deleted_at)
SELECT
  project_id, 'email_domain', address, provider, provider_identity_id,
  verified, verification_status, dkim_status,
  inbound_bootstrapped, inbound_config,
  notes, metadata, last_synced_at, last_sync_error,
  created_at, updated_at, deleted_at
FROM senders WHERE kind = 'domain';

-- Add the inheritance FK column to senders.
ALTER TABLE senders ADD COLUMN parent_identity_id INTEGER;

CREATE INDEX idx_senders_parent_identity
  ON senders (parent_identity_id)
  WHERE deleted_at IS NULL AND parent_identity_id IS NOT NULL;

-- Backfill: for each mailbox (kind='email' today, soon-to-be
-- 'email_mailbox'), find its parent_identity by matching the address
-- suffix against the just-moved domain rows. Same suffix logic v0.11.3
-- used at refresh time; we persist the edge so future refreshes can
-- use the FK directly.
UPDATE senders
SET parent_identity_id = (
  SELECT id FROM identities
  WHERE identities.project_id = senders.project_id
    AND identities.kind = 'email_domain'
    AND identities.deleted_at IS NULL
    AND instr(senders.address, '@' || identities.address)
        = (length(senders.address) - length(identities.address))
)
WHERE kind = 'email' AND senders.address LIKE '%@%';

-- Rename kind='email' → 'email_mailbox' so the senders table's enum
-- is self-describing across all (current + future) channels.
UPDATE senders SET kind = 'email_mailbox' WHERE kind = 'email';

-- Drop the rows we moved into identities. Data is preserved upstream;
-- if rollback is ever needed, the moved rows can be re-derived from
-- identities WHERE kind='email_domain'.
DELETE FROM senders WHERE kind = 'domain';
