CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_chats_ntfy_topic
    ON channels_chats(channel, thread_id)
    WHERE channel = 'ntfy';
