-- v0.7.1: per-row "force_probe" flag for media_reindex(force=true).
--
-- When set to 1, processOne bypasses max_probe_size_mb for that one
-- row — the operator has explicitly asked us to probe a huge file
-- and accepted the temp-disk cost. processOne clears the flag on
-- both success and failure so a single force is genuinely one-shot.

ALTER TABLE media ADD COLUMN force_probe INTEGER NOT NULL DEFAULT 0;

-- Cheap partial index — reindex updates this column rarely; the
-- indexer's tick reads only rows where probe_status='pending', so
-- we don't need to index this independently. The default 0 keeps
-- existing rows unaffected.
