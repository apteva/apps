-- CRM ↔ messaging coupling.
--
-- Adds conversation threading so inbound replies group with the
-- original outbound, instead of appearing as flat unrelated rows on
-- the activity timeline. Mirrors the conversation model every modern
-- CRM (HubSpot, Front, Attio) ships with.
--
-- Conversations are nullable on activities — system events, notes,
-- calls, meetings stay outside any conversation; only message-shaped
-- activities (email_*, sms_*, whatsapp_*) link to one.
--
-- This migration is benign without the messaging app installed. The
-- messaging dependency is soft (apteva.yaml: required=false); these
-- tables sit empty until something starts logging messages.

CREATE TABLE contact_conversations (
  id                INTEGER PRIMARY KEY,
  project_id        TEXT    NOT NULL,
  contact_id        INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  channel           TEXT    NOT NULL,            -- email | sms | whatsapp
  subject           TEXT,                         -- email only; first-message subject
  root_message_id   TEXT,                         -- Message-Id of the chain root (email)
  started_at        TIMESTAMP NOT NULL,
  last_activity_at  TIMESTAMP NOT NULL
);
CREATE INDEX ix_conv_contact  ON contact_conversations(project_id, contact_id, last_activity_at DESC);
CREATE INDEX ix_conv_root_msg ON contact_conversations(project_id, root_message_id) WHERE root_message_id IS NOT NULL;
-- One persistent conversation per (contact, channel) for SMS/WhatsApp.
-- Email allows many (one per thread). Enforced application-side because
-- the constraint is conditional on channel.

ALTER TABLE contact_activities ADD COLUMN conversation_id   INTEGER;
ALTER TABLE contact_activities ADD COLUMN message_id_header TEXT;
ALTER TABLE contact_activities ADD COLUMN messaging_id      INTEGER;

-- Activity-by-conversation: drives the conversation-detail view.
CREATE INDEX ix_act_conv ON contact_activities(project_id, conversation_id, occurred_at DESC) WHERE conversation_id IS NOT NULL;
-- Activity-by-Message-Id: lets inbound In-Reply-To find the prior outbound
-- without a JSON-extract on source_detail.
CREATE INDEX ix_act_msgid ON contact_activities(project_id, message_id_header) WHERE message_id_header IS NOT NULL;
-- Inbound dedup: messaging may redeliver the same message_id on retry.
-- One row per (project, messaging_id) prevents double-logging.
CREATE UNIQUE INDEX ux_act_messaging_id ON contact_activities(project_id, messaging_id) WHERE messaging_id IS NOT NULL;
