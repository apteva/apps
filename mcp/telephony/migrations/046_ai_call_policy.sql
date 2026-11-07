-- Only newly spawned AI sessions get a policy. Existing and human calls are untouched.
CREATE TABLE ai_call_policies (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 thread_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0,
 stage TEXT NOT NULL DEFAULT 'prepared',
 state_json TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX ai_call_policies_active ON ai_call_policies(project_id,stage)
 WHERE stage NOT IN ('ended','disarmed');
