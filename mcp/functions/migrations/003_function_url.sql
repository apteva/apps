-- Functions v1.5 — optional public Function URL config.
--
-- Stored on the existing functions row so enabling/disabling a direct
-- URL stays part of ordinary function metadata. No separate endpoint
-- or token table is introduced.

ALTER TABLE functions ADD COLUMN function_url_json TEXT;
