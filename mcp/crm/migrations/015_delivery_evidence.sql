-- Durable failures are independent of Messaging's suppression overlay.
ALTER TABLE contact_channel_delivery_state ADD COLUMN delivery_evidence TEXT;
UPDATE contact_channel_delivery_state SET delivery_evidence=status WHERE status IN ('hard_bounced','complained');
