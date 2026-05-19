-- Per-generation cost tracking. Looked up at completion time from the
-- provider's published per-model rate (Venice exposes via GET /models
-- under model_spec.pricing — generation.usd, resolutions[tier].usd, or
-- inpaint.usd). For providers without published rates the column stays 0.

ALTER TABLE generations ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE video_jobs  ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
