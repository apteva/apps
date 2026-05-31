-- Normalized conversation participants for inbox views.
--
-- The activity log keeps message payload details in source_detail for
-- auditability, but inbox filters need indexed address lookups across
-- channels. This table records the participants a conversation has seen:
-- sender (from), recipients (to/cc/bcc), channel, address, and email domain.

CREATE TABLE conversation_participants (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  conversation_id INTEGER NOT NULL REFERENCES contact_conversations(id) ON DELETE CASCADE,
  contact_id      INTEGER REFERENCES contacts(id) ON DELETE SET NULL,
  role            TEXT    NOT NULL,       -- from | to | cc | bcc
  channel         TEXT    NOT NULL,       -- email | sms | whatsapp
  address         TEXT    NOT NULL,
  domain          TEXT,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ux_conv_participant
  ON conversation_participants(project_id, conversation_id, role, address);
CREATE INDEX ix_conv_participant_addr
  ON conversation_participants(project_id, role, address);
CREATE INDEX ix_conv_participant_domain
  ON conversation_participants(project_id, role, domain);

-- Best-effort backfill for existing threaded activity.
-- Inbound "from" was implicit in the matched contact, while "to" was
-- preserved in source_detail by the inbound webhook.
INSERT OR IGNORE INTO conversation_participants
  (project_id, conversation_id, contact_id, role, channel, address, domain)
SELECT
  ca.project_id,
  ca.conversation_id,
  ca.contact_id,
  'from',
  cc.channel,
  CASE
    WHEN cc.channel = 'email' THEN LOWER(TRIM(COALESCE(c.primary_email, '')))
    ELSE TRIM(COALESCE(c.primary_phone, ''))
  END,
  CASE
    WHEN cc.channel = 'email' AND INSTR(LOWER(COALESCE(c.primary_email, '')), '@') > 0
      THEN SUBSTR(LOWER(c.primary_email), INSTR(LOWER(c.primary_email), '@') + 1)
    ELSE NULL
  END
FROM contact_activities ca
JOIN contact_conversations cc ON cc.project_id = ca.project_id AND cc.id = ca.conversation_id
JOIN contacts c ON c.project_id = ca.project_id AND c.id = ca.contact_id
WHERE ca.conversation_id IS NOT NULL
  AND ca.kind IN ('email_received', 'sms_received', 'whatsapp_received')
  AND (
    (cc.channel = 'email' AND COALESCE(c.primary_email, '') <> '')
    OR (cc.channel <> 'email' AND COALESCE(c.primary_phone, '') <> '')
  );

INSERT OR IGNORE INTO conversation_participants
  (project_id, conversation_id, contact_id, role, channel, address, domain)
SELECT
  ca.project_id,
  ca.conversation_id,
  NULL,
  'to',
  cc.channel,
  LOWER(TRIM(CAST(j.value AS TEXT))),
  CASE
    WHEN cc.channel = 'email' AND INSTR(LOWER(CAST(j.value AS TEXT)), '@') > 0
      THEN SUBSTR(LOWER(CAST(j.value AS TEXT)), INSTR(LOWER(CAST(j.value AS TEXT)), '@') + 1)
    ELSE NULL
  END
FROM contact_activities ca
JOIN contact_conversations cc ON cc.project_id = ca.project_id AND cc.id = ca.conversation_id
JOIN json_each(ca.source_detail, '$.to') AS j
WHERE ca.conversation_id IS NOT NULL
  AND ca.source_detail IS NOT NULL
  AND TRIM(CAST(j.value AS TEXT)) <> '';
