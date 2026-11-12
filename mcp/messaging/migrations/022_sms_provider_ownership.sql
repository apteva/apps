-- Operator policy is independent of provider verification and survives refresh.
ALTER TABLE senders ADD COLUMN outbound_enabled INTEGER NOT NULL DEFAULT 1;
CREATE INDEX ix_sender_phone_owner ON senders(provider,provider_connection_id,channel,address) WHERE deleted_at IS NULL;
CREATE INDEX ix_message_provider_account ON messages(provider_slug,provider_connection_id,provider_message_id);
