-- Existing stored recordings retain their original format; future imports default to MP3.
ALTER TABLE recording_settings ADD COLUMN storage_format TEXT NOT NULL DEFAULT 'mp3';
ALTER TABLE recordings ADD COLUMN storage_format TEXT NOT NULL DEFAULT '';
ALTER TABLE recordings ADD COLUMN source_size_bytes INTEGER NOT NULL DEFAULT 0;
