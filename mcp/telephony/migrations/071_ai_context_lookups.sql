-- AI context lookups: one row per invocation of an AI destination's context
-- function before the agent starts. The validated response (facts and
-- optional greeting) is retained with the call for diagnostics.
CREATE TABLE IF NOT EXISTS ai_context_lookups (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL,
    call_id        TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    node_id        TEXT NOT NULL DEFAULT '',
    destination_id TEXT NOT NULL DEFAULT '',
    function_id    INTEGER NOT NULL,
    status         TEXT NOT NULL,
    reason         TEXT NOT NULL DEFAULT '',
    request_json   TEXT NOT NULL DEFAULT '{}',
    response_json  TEXT NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ai_context_lookups_call
    ON ai_context_lookups(project_id, call_id, created_at);
