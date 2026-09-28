ALTER TABLE calls ADD COLUMN carrier_leg_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN carrier_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN carrier_signaling_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE routing_effects (
    id TEXT PRIMARY KEY,
    call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    plan_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    stage TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_routing_effects_due ON routing_effects(project_id,status,next_attempt_at);

CREATE TABLE carrier_command_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    command TEXT NOT NULL,
    command_id TEXT NOT NULL DEFAULT '',
    response_status INTEGER NOT NULL DEFAULT 0,
    succeeded INTEGER NOT NULL DEFAULT 0,
    occurred_at TEXT NOT NULL
);
CREATE INDEX idx_carrier_command_events_call ON carrier_command_events(project_id,call_id,id);
