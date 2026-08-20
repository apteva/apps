-- conversations v0.12: native Telegram thinking and private-chat drafts.
ALTER TABLE telegram_connections ADD COLUMN response_feedback TEXT NOT NULL DEFAULT 'live';
