-- Avatar (talking-head) support. The video_jobs async-poll table now
-- also tracks avatar jobs — same lifecycle (queue → poll → store →
-- emit), different provider tool + retrieve shape. Two new columns
-- discriminate; existing rows backfill to the video defaults so the
-- worker keeps polling them unchanged.

ALTER TABLE video_jobs ADD COLUMN kind TEXT NOT NULL DEFAULT 'video';
ALTER TABLE video_jobs ADD COLUMN role TEXT NOT NULL DEFAULT 'video_provider';
