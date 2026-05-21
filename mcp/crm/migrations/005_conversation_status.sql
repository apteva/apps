-- Conversation lifecycle status — ticket-flavored, but scoped to what an
-- agent-driven CRM can actually back.
--
-- States (mirrors Intercom's conversation model, minus snooze):
--   open    — needs action from us (default; ball in our court)
--   pending — waiting on the contact's reply (ball in their court)
--   closed  — resolved
--
-- We deliberately DON'T ship 'snoozed' (deferral-with-wake-time is a
-- follow-up task, not a thread state — it would need a background wake
-- mechanism for little gain here) nor Zendesk's pending/on-hold/solved
-- granularity (those presuppose an agent/queue/SLA model this CRM has
-- none of). Adding 'snoozed' later is a purely additive migration.
--
-- priority orders the queue in the panel. No assignment/SLA workflow
-- sits behind it — it's a sort key + a badge.
--
-- Behavior wired in messaging.go: an inbound reply auto-reopens a
-- pending/closed conversation back to 'open' (the contact responded, so
-- it's on us again). Existing rows default to 'open' — i.e. exactly
-- today's implicit behavior.

ALTER TABLE contact_conversations ADD COLUMN status TEXT NOT NULL DEFAULT 'open';
ALTER TABLE contact_conversations ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE contact_conversations ADD COLUMN status_changed_at TIMESTAMP;

-- Drives the panel's queue view (status filter + recency sort) and the
-- agent's contacts_list_conversations status filter.
CREATE INDEX ix_conv_status ON contact_conversations(project_id, status, last_activity_at DESC);
