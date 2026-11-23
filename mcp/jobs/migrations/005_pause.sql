-- Pause/resume (v0.2.0). paused_at marks a pause request. A pending job
-- moves to status='paused' immediately; a job that is mid-delivery keeps
-- status='running' until its attempt finalizes and then parks as 'paused'.
-- Existing rows get NULL, so every current job stays active.
ALTER TABLE jobs ADD COLUMN paused_at TIMESTAMP;
