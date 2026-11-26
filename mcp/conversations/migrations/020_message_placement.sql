-- A queued message is shown where it was released, not where it was typed.
-- placed_after holds the last visible message id at release (or steer) time;
-- 0 means "at its own id". Ids, cursors and deliveries are unchanged.
ALTER TABLE messages ADD COLUMN placed_after INTEGER NOT NULL DEFAULT 0;
