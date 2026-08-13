-- Remember where Jobs registered each policy so cancellation does not depend
-- on whichever dashboard project happens to be selected later.
ALTER TABLE policies ADD COLUMN jobs_project_id TEXT NOT NULL DEFAULT '';

-- SHA-256 is calculated over the stored object. This flag tells restore to
-- decrypt it with the configured age passphrase after integrity verification.
ALTER TABLE runs ADD COLUMN encrypted INTEGER NOT NULL DEFAULT 0;

-- Keep historical destination configuration available for restore.
ALTER TABLE destinations ADD COLUMN deleted_at TEXT NOT NULL DEFAULT '';
