CREATE TABLE process_triggers (
 id TEXT PRIMARY KEY, process_id TEXT NOT NULL REFERENCES processes(id), assignment_id TEXT NOT NULL REFERENCES process_assignments(id),
 project_id TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1, config_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'paused', subscription_revision INTEGER NOT NULL DEFAULT 1,
 subscription_enabled INTEGER NOT NULL DEFAULT 0, sync_pending INTEGER NOT NULL DEFAULT 1, sync_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX process_triggers_assignment ON process_triggers(assignment_id);
CREATE TABLE process_trigger_events (
 id TEXT PRIMARY KEY, trigger_id TEXT NOT NULL REFERENCES process_triggers(id), project_id TEXT NOT NULL,
 event_id TEXT NOT NULL, delivery_id TEXT NOT NULL, trigger_json TEXT NOT NULL, event_json TEXT NOT NULL,
 status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', parameters_json TEXT NOT NULL DEFAULT '{}',
 run_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 UNIQUE(trigger_id,event_id)
);
CREATE INDEX process_trigger_history ON process_trigger_events(trigger_id,created_at DESC);
ALTER TABLE process_runs ADD COLUMN trigger_event_id TEXT NOT NULL DEFAULT '';

-- Invalidate queued deliveries in the same transaction as a parent status
-- change, even if pause and resume both happen before the reconciliation tick.
CREATE TRIGGER process_trigger_process_status AFTER UPDATE OF status ON processes
WHEN OLD.status <> NEW.status
BEGIN
 UPDATE process_triggers SET subscription_revision=subscription_revision+1,sync_pending=1
 WHERE process_id=NEW.id;
END;
CREATE TRIGGER process_trigger_assignment_status AFTER UPDATE OF status ON process_assignments
WHEN OLD.status <> NEW.status
BEGIN
 UPDATE process_triggers SET subscription_revision=subscription_revision+1,sync_pending=1
 WHERE assignment_id=NEW.id;
END;
