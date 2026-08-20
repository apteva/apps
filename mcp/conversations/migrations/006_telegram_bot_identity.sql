-- conversations v0.11: make a dedicated Telegram bot look like the sole
-- routed agent while preserving the operator-defined BotFather identity.
ALTER TABLE telegram_connections ADD COLUMN original_bot_name TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_connections ADD COLUMN auto_name_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE telegram_connections ADD COLUMN synced_agent_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_connections ADD COLUMN synced_bot_name TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_connections ADD COLUMN name_sync_error TEXT NOT NULL DEFAULT '';
