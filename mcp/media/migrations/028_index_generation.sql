ALTER TABLE media ADD COLUMN derivation_retry_reason TEXT;
ALTER TABLE media ADD COLUMN derivation_source_sha256 TEXT;
DELETE FROM derivation_cleanup_queue WHERE json_extract(derivation,'$.kind') NOT IN ('thumbnail','waveform','keyframe');
ALTER TABLE derivations ADD COLUMN source_sha256 TEXT;
