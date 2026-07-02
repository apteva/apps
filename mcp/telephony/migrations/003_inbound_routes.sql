ALTER TABLE calls ADD COLUMN direction TEXT NOT NULL DEFAULT 'outbound';
ALTER TABLE calls ADD COLUMN agent_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE calls ADD COLUMN route_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_calls_agent ON calls(agent_id, status);
CREATE INDEX IF NOT EXISTS idx_calls_route ON calls(route_id, placed_at DESC);

CREATE TABLE IF NOT EXISTS inbound_routes (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL DEFAULT '',
    carrier_slug          TEXT NOT NULL,
    carrier_connection_id INTEGER NOT NULL DEFAULT 0,
    phone_number          TEXT NOT NULL,
    phone_number_sid      TEXT NOT NULL DEFAULT '',
    agent_id              INTEGER NOT NULL,
    enabled               INTEGER NOT NULL DEFAULT 1,
    hold_prompt           TEXT NOT NULL DEFAULT '',
    timeout_sec           INTEGER NOT NULL DEFAULT 60,
    secret                TEXT NOT NULL,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_inbound_routes_project ON inbound_routes(project_id, phone_number);
CREATE INDEX IF NOT EXISTS idx_inbound_routes_agent ON inbound_routes(agent_id, enabled);
