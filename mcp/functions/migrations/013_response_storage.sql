-- Per-function response storage policy. 'auto' keeps unauthenticated
-- responses and omits authenticated ones (status, size and timing only).
ALTER TABLE functions ADD COLUMN store_responses TEXT NOT NULL DEFAULT 'auto'
 CHECK (store_responses IN ('auto', 'never', 'errors_only', 'always'));
-- Size is recorded even when the body is not. response_omitted names why a
-- body was not stored (NULL when it was, or for rows written before 013).
ALTER TABLE function_invocations ADD COLUMN response_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE function_invocations ADD COLUMN response_omitted TEXT;
