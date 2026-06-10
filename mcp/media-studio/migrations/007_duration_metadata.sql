-- Duration metadata distinguishes pre-generation timeline estimates
-- from post-generation probed media duration.

ALTER TABLE generations ADD COLUMN estimated_duration_seconds REAL NOT NULL DEFAULT 0;
ALTER TABLE generations ADD COLUMN actual_duration_seconds    REAL NOT NULL DEFAULT 0;

ALTER TABLE video_jobs ADD COLUMN estimated_duration_seconds REAL NOT NULL DEFAULT 0;
ALTER TABLE video_jobs ADD COLUMN actual_duration_seconds    REAL NOT NULL DEFAULT 0;
