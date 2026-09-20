-- Distinguish provider-confirmed missing values from interrupted/failed work.
-- Unavailable fields are terminal for the current run, but an operator may
-- explicitly resume the job to ask the provider again later.

ALTER TABLE keyword_metric_job_items
    ADD COLUMN volume_unavailable INTEGER NOT NULL DEFAULT 0;

ALTER TABLE keyword_metric_job_items
    ADD COLUMN difficulty_unavailable INTEGER NOT NULL DEFAULT 0;

DROP INDEX IF EXISTS idx_keyword_metric_job_items_pending;

CREATE INDEX IF NOT EXISTS idx_keyword_metric_job_items_pending
    ON keyword_metric_job_items(
        job_id,
        volume_done,
        volume_unavailable,
        difficulty_done,
        difficulty_unavailable,
        keyword_id
    );
