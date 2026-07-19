-- Persist user-selected file priorities and resumable storage handoff state.

ALTER TABLE torrents
    ADD COLUMN upload_progress_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE torrents
    ADD COLUMN file_priorities_json TEXT NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_torrents_infohash ON torrents(infohash);
