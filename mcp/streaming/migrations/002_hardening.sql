-- Streaming v0.2 — hardening.
--
-- 1. Signed, expiring playback URLs. Every stream gets its own HMAC
--    secret (url_signing_secret) and a policy flag
--    (require_signed_urls). v0.1's playback_token was a static bearer
--    string with unlimited lifetime, so a consumer app's "replay
--    expires" policy was trivially bypassed by reusing the raw media
--    URL. See signing.go for the wire contract.
--
-- 2. Retention bookkeeping. pruned_at marks a stream whose media the
--    retention-sweeper has already reclaimed, so the hourly sweep
--    doesn't re-walk every finished stream forever. retention_days
--    existed in v0.1 but nothing ever read it.
--
-- 3. Timestamp normalization. v0.1 wrote a MIX of RFC3339 ("…T…Z",
--    from the Go paths) and SQLite CURRENT_TIMESTAMP ("YYYY-MM-DD
--    HH:MM:SS", from the watchdog + the OnMount reconciler) into the
--    same columns, so any consumer that parsed or lexically sorted
--    them broke depending on which code path had run. v0.2 writes
--    RFC3339 UTC from Go everywhere; this backfills the old rows.
--
-- SQLite has no `ALTER TABLE … ADD COLUMN IF NOT EXISTS`; the SDK's
-- _migrations ledger is what makes this file run exactly once. The
-- statements that do support IF NOT EXISTS use it.

ALTER TABLE streams ADD COLUMN url_signing_secret  TEXT    NOT NULL DEFAULT '';
ALTER TABLE streams ADD COLUMN require_signed_urls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE streams ADD COLUMN pruned_at           TIMESTAMP;

-- Backfill: an empty secret must never validate a signature, so give
-- pre-v0.2 rows a real (SQLite-generated) one. Go regenerates a
-- crypto/rand secret lazily for any row still holding '' the first
-- time it signs — see ensureSigningSecret.
UPDATE streams
   SET url_signing_secret = hex(randomblob(24))
 WHERE url_signing_secret IS NULL OR url_signing_secret = '';

-- "2026-08-18 09:00:00" → "2026-08-18T09:00:00Z". CURRENT_TIMESTAMP is
-- already UTC, so the Z is accurate.
UPDATE streams SET created_at = replace(created_at, ' ', 'T') || 'Z'
 WHERE created_at IS NOT NULL AND created_at <> '' AND created_at NOT LIKE '%T%';
UPDATE streams SET started_at = replace(started_at, ' ', 'T') || 'Z'
 WHERE started_at IS NOT NULL AND started_at <> '' AND started_at NOT LIKE '%T%';
UPDATE streams SET ended_at = replace(ended_at, ' ', 'T') || 'Z'
 WHERE ended_at IS NOT NULL AND ended_at <> '' AND ended_at NOT LIKE '%T%';
UPDATE stream_events SET occurred_at = replace(occurred_at, ' ', 'T') || 'Z'
 WHERE occurred_at IS NOT NULL AND occurred_at <> '' AND occurred_at NOT LIKE '%T%';

-- The retention sweeper's candidate scan.
CREATE INDEX IF NOT EXISTS ix_stream_retention ON streams(status, pruned_at);
