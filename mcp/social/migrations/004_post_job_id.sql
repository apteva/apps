-- Track the jobs.id created when a post is scheduled. Lets
-- post_reschedule and post_delete cancel the right job without
-- needing a list-by-idempotency-key call against the jobs app.
-- 0 = no job (post never scheduled, or already published).
ALTER TABLE posts ADD COLUMN job_id INTEGER NOT NULL DEFAULT 0;
