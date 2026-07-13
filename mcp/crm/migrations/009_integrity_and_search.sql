-- Integrity and search hardening.

-- Older releases enforced one SMS/WhatsApp conversation per contact only in
-- application code. Consolidate any historical duplicates before adding the
-- database constraint used to serialize concurrent first messages.
CREATE TEMP TABLE crm_persistent_conversation_merge (
  duplicate_id INTEGER PRIMARY KEY,
  keeper_id    INTEGER NOT NULL
);

INSERT INTO crm_persistent_conversation_merge (duplicate_id, keeper_id)
SELECT c.id, (
  SELECT MIN(k.id)
  FROM contact_conversations k
  WHERE k.project_id = c.project_id
    AND k.contact_id = c.contact_id
    AND k.channel = c.channel
)
FROM contact_conversations c
WHERE c.channel IN ('sms', 'whatsapp')
  AND c.id <> (
    SELECT MIN(k.id)
    FROM contact_conversations k
    WHERE k.project_id = c.project_id
      AND k.contact_id = c.contact_id
      AND k.channel = c.channel
  );

UPDATE contact_activities
SET conversation_id = (
  SELECT keeper_id
  FROM crm_persistent_conversation_merge
  WHERE duplicate_id = contact_activities.conversation_id
)
WHERE conversation_id IN (
  SELECT duplicate_id FROM crm_persistent_conversation_merge
);

INSERT OR IGNORE INTO conversation_participants
  (project_id, conversation_id, contact_id, role, channel, address, domain, created_at)
SELECT p.project_id, m.keeper_id, p.contact_id, p.role, p.channel, p.address, p.domain, p.created_at
FROM conversation_participants p
JOIN crm_persistent_conversation_merge m ON m.duplicate_id = p.conversation_id;

DELETE FROM conversation_participants
WHERE conversation_id IN (SELECT duplicate_id FROM crm_persistent_conversation_merge);

DELETE FROM contact_conversations
WHERE id IN (SELECT duplicate_id FROM crm_persistent_conversation_merge);

DROP TABLE crm_persistent_conversation_merge;

CREATE UNIQUE INDEX ux_conv_persistent_channel
  ON contact_conversations(project_id, contact_id, channel)
  WHERE channel IN ('sms', 'whatsapp');

-- Timestamp filters use SQLite datetime() to compare legacy RFC3339 and
-- CURRENT_TIMESTAMP values consistently. Matching expression indexes keep
-- those predicates sargable for large global installs.
CREATE INDEX ix_contacts_created_datetime
  ON contacts(project_id, datetime(created_at), id);
CREATE INDEX ix_contacts_updated_datetime
  ON contacts(project_id, datetime(updated_at), id);
