ALTER TABLE ad_audience_jobs ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE ad_audience_jobs ADD COLUMN lease_expires_at TEXT NOT NULL DEFAULT '';

CREATE TABLE ad_audience_batches (
    job_id INTEGER NOT NULL REFERENCES ad_audience_jobs(id) ON DELETE CASCADE,
    batch_start INTEGER NOT NULL,
    batch_end INTEGER NOT NULL,
    provider_request_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    PRIMARY KEY(job_id, batch_start)
);

-- Legacy multi-batch uploads retained only their final request ID. Their
-- earlier results cannot be verified; require a fresh sync instead of
-- reporting them ready based solely on the final batch.
UPDATE ad_audience_jobs SET status='failed',
    last_error='Legacy audience batch diagnostics are incomplete; submit a new sync with a new idempotency key',
    completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
WHERE status='provider_processing' AND (processed_rows>1000 OR provider_request_id='');

-- Single-batch legacy jobs still have sufficient diagnostics to finish.
INSERT INTO ad_audience_batches(job_id, batch_start, batch_end, provider_request_id)
SELECT id, 0, processed_rows, provider_request_id FROM ad_audience_jobs
WHERE status='provider_processing' AND provider_request_id!='';

-- Older in-progress jobs have no durable batch ledger. Replay add/remove
-- from the beginning, tracking every batch under the new format.
UPDATE ad_audience_jobs SET status='queued', processed_rows=0, accepted_rows=0,
    source_checksum='', provider_request_id='', available_at=datetime('now')
WHERE status IN ('queued','processing');
