-- Normalize timestamp offsets for half-open call-start cohorts and cursor reads.
-- Statistics read compact audio summaries, never full diagnostics/history.
CREATE INDEX idx_calls_statistics_start ON calls(project_id,unixepoch(placed_at),id);

-- One-time reindex of retained evidence. The existing 100-call background batch
-- enriches compact summaries; no synchronous full diagnostic scan on page reads.
INSERT OR IGNORE INTO telephony_audio_pending SELECT call_id FROM telephony_audio_reports;
