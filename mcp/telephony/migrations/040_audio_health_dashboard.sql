-- Dashboard reads use an indexed summary, never scan/decode the call history.
-- Diagnostic writers enqueue a primary key; the existing telemetry worker
-- computes summaries off all media read/write paths in bounded batches.
CREATE TABLE telephony_audio_reports (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, observed_ms INTEGER NOT NULL,
 provider TEXT NOT NULL, has_issues INTEGER NOT NULL,
 issue_codes TEXT NOT NULL, stage_codes TEXT NOT NULL,
 summary_json TEXT NOT NULL
);
CREATE INDEX telephony_audio_reports_window ON telephony_audio_reports(project_id,observed_ms DESC,call_id DESC);
CREATE INDEX telephony_audio_reports_provider ON telephony_audio_reports(project_id,provider,observed_ms DESC,call_id DESC);
CREATE INDEX telephony_audio_reports_issues ON telephony_audio_reports(project_id,has_issues,observed_ms DESC,call_id DESC);
CREATE TABLE telephony_audio_pending (call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE);
INSERT INTO telephony_audio_pending SELECT id FROM calls WHERE peer_kind='human';
CREATE TRIGGER telephony_audio_insert AFTER INSERT ON calls WHEN NEW.peer_kind='human' BEGIN
 INSERT OR IGNORE INTO telephony_audio_pending VALUES(NEW.id);
END;
CREATE TRIGGER telephony_audio_update AFTER UPDATE OF browser_audio_diagnostics,carrier_audio_diagnostics,media_error_message,media_close_code,media_close_reason,peer_kind ON calls BEGIN
 INSERT OR IGNORE INTO telephony_audio_pending VALUES(NEW.id);
END;
CREATE TABLE telephony_audio_alert_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL,
 occurred_ms INTEGER NOT NULL, provider TEXT NOT NULL, stage TEXT NOT NULL,
 payload_json TEXT NOT NULL
);
CREATE INDEX telephony_audio_alert_history_window ON telephony_audio_alert_history(project_id,occurred_ms DESC,id DESC);

CREATE TRIGGER telephony_audio_delete AFTER DELETE ON calls BEGIN
 DELETE FROM telephony_audio_pending WHERE call_id=OLD.id;
 DELETE FROM telephony_audio_reports WHERE call_id=OLD.id;
END;
