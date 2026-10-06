-- Separate connected duration from setup and media-recovery watchdogs.
ALTER TABLE calls ADD COLUMN max_duration_sec INTEGER NOT NULL DEFAULT 14400;
ALTER TABLE calls ADD COLUMN duration_started_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN connected_deadline_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN media_recovery_timeout_sec INTEGER NOT NULL DEFAULT 120;
ALTER TABLE calls ADD COLUMN media_deadline_at TEXT NOT NULL DEFAULT '';

-- Preserve inferred legacy per-call durations, including one hour: the old
-- database cannot distinguish an explicit one-hour override from its default.
-- New calls use four hours; already-issued carrier limits cannot be recalled.
UPDATE calls SET max_duration_sec=CASE
 WHEN ROUND((julianday(deadline_at)-julianday(placed_at))*86400) BETWEEN 60 AND 14400
 THEN CAST(ROUND((julianday(deadline_at)-julianday(placed_at))*86400) AS INTEGER)
 ELSE 14400 END;
UPDATE calls SET duration_started_at=CASE
 WHEN carrier_answered_at<>'' AND (direction='outbound' OR media_connected_at='' OR julianday(carrier_answered_at)<=julianday(media_connected_at)) THEN carrier_answered_at
 ELSE media_connected_at END
 WHERE carrier_answered_at<>'' OR (direction='inbound' AND media_connected_at<>'');
UPDATE calls SET connected_deadline_at=strftime('%Y-%m-%dT%H:%M:%fZ',duration_started_at,'+'||max_duration_sec||' seconds') WHERE duration_started_at<>'';
UPDATE calls SET media_deadline_at=strftime('%Y-%m-%dT%H:%M:%fZ',COALESCE(NULLIF(media_disconnected_at,''),'now'),'+120 seconds')
 WHERE status NOT IN ('completed','failed','busy','no-answer','canceled') AND media_status IN ('error','disconnected') AND media_connected_at<>'';

-- The first positive carrier/media observation starts the persisted clock.
-- Reconnect, handoff, duplicate callbacks and process restart cannot extend it.
CREATE TRIGGER calls_connected_duration AFTER UPDATE OF carrier_answered_at,media_connected_at ON calls
 WHEN NEW.duration_started_at='' AND (NEW.carrier_answered_at<>'' OR (NEW.direction='inbound' AND NEW.media_connected_at<>''))
 AND NEW.status NOT IN ('completed','failed','busy','no-answer','canceled')
BEGIN
 UPDATE calls SET duration_started_at=CASE
  WHEN NEW.carrier_answered_at<>'' AND (NEW.direction='outbound' OR NEW.media_connected_at='' OR julianday(NEW.carrier_answered_at)<=julianday(NEW.media_connected_at)) THEN NEW.carrier_answered_at
  ELSE NEW.media_connected_at END WHERE id=NEW.id;
 UPDATE calls SET connected_deadline_at=strftime('%Y-%m-%dT%H:%M:%fZ',duration_started_at,'+'||max_duration_sec||' seconds') WHERE id=NEW.id;
END;

CREATE TRIGGER calls_media_recovery AFTER UPDATE OF media_status ON calls
 WHEN NEW.status NOT IN ('completed','failed','busy','no-answer','canceled')
BEGIN
 UPDATE calls SET media_deadline_at=CASE
  WHEN NEW.media_status='connected' THEN ''
  WHEN NEW.media_status='error' OR (NEW.media_status='disconnected' AND NEW.media_connected_at<>'')
   THEN COALESCE(NULLIF(media_deadline_at,''),strftime('%Y-%m-%dT%H:%M:%fZ','now','+'||media_recovery_timeout_sec||' seconds'))
  ELSE media_deadline_at END WHERE id=NEW.id;
END;
CREATE INDEX idx_calls_connected_deadline ON calls(project_id,connected_deadline_at)
 WHERE status NOT IN ('completed','failed','busy','no-answer','canceled');
