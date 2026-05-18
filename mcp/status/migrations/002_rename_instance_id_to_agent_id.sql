-- Rename instance_id → agent_id to match the platform terminology
-- adopted across the other apps (tasks etc). SQLite's
-- ALTER TABLE RENAME COLUMN (3.25.0+) handles PRIMARY KEY columns
-- correctly — no table rebuild needed here.
ALTER TABLE status_status RENAME COLUMN instance_id TO agent_id;
