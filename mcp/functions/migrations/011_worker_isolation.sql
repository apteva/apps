-- Keep the previous authenticated-only isolation on existing and older-writer rows.
ALTER TABLE functions ADD COLUMN worker_isolation TEXT NOT NULL DEFAULT 'authenticated_invocation'
 CHECK (worker_isolation IN ('function_version', 'invocation', 'authenticated_invocation'));
