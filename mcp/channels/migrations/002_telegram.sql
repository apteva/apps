CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_telegram_topic
    ON channels(json_extract(config_json, '$.topic'))
    WHERE type = 'telegram';
