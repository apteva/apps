CREATE TABLE IF NOT EXISTS routing_flows (
    id                   TEXT PRIMARY KEY,
    project_id           TEXT NOT NULL,
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    draft_json           TEXT NOT NULL,
    published_version_id TEXT NOT NULL DEFAULT '',
    generated            INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_routing_flows_project
    ON routing_flows(project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS routing_flow_versions (
    id          TEXT PRIMARY KEY,
    flow_id     TEXT NOT NULL REFERENCES routing_flows(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL,
    version     INTEGER NOT NULL,
    definition  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    UNIQUE(flow_id, version)
);

CREATE INDEX IF NOT EXISTS idx_routing_flow_versions_flow
    ON routing_flow_versions(flow_id, version DESC);

CREATE TABLE IF NOT EXISTS routing_destinations (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_routing_destinations_project
    ON routing_destinations(project_id, kind, name);

CREATE TABLE IF NOT EXISTS ring_groups (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL,
    name              TEXT NOT NULL,
    strategy          TEXT NOT NULL DEFAULT 'simultaneous',
    timeout_sec       INTEGER NOT NULL DEFAULT 20,
    overflow_node_id  TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ring_groups_project
    ON ring_groups(project_id, name);

CREATE TABLE IF NOT EXISTS ring_group_members (
    ring_group_id  TEXT NOT NULL REFERENCES ring_groups(id) ON DELETE CASCADE,
    destination_id TEXT NOT NULL REFERENCES routing_destinations(id) ON DELETE CASCADE,
    position       INTEGER NOT NULL DEFAULT 0,
    priority       INTEGER NOT NULL DEFAULT 0,
    weight         INTEGER NOT NULL DEFAULT 1,
    timeout_sec    INTEGER NOT NULL DEFAULT 20,
    enabled        INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(ring_group_id, destination_id)
);

CREATE INDEX IF NOT EXISTS idx_ring_group_members_order
    ON ring_group_members(ring_group_id, priority, position);

CREATE TABLE IF NOT EXISTS call_route_executions (
    id              TEXT PRIMARY KEY,
    call_id         TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id      TEXT NOT NULL,
    flow_id         TEXT NOT NULL,
    flow_version_id TEXT NOT NULL,
    status          TEXT NOT NULL,
    current_node_id TEXT NOT NULL DEFAULT '',
    selected_destination_id TEXT NOT NULL DEFAULT '',
    context_json    TEXT NOT NULL DEFAULT '{}',
    started_at      TEXT NOT NULL,
    ended_at        TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_call_route_execution_call
    ON call_route_executions(call_id);

CREATE INDEX IF NOT EXISTS idx_call_route_execution_project
    ON call_route_executions(project_id, started_at DESC);

CREATE TABLE IF NOT EXISTS call_node_executions (
    id              TEXT PRIMARY KEY,
    execution_id    TEXT NOT NULL REFERENCES call_route_executions(id) ON DELETE CASCADE,
    node_id         TEXT NOT NULL,
    node_type       TEXT NOT NULL,
    outcome         TEXT NOT NULL DEFAULT '',
    detail_json     TEXT NOT NULL DEFAULT '{}',
    entered_at      TEXT NOT NULL,
    exited_at       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_call_node_execution_trace
    ON call_node_executions(execution_id, entered_at);

CREATE TABLE IF NOT EXISTS call_legs (
    id                    TEXT PRIMARY KEY,
    call_id               TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id            TEXT NOT NULL,
    destination_id        TEXT NOT NULL DEFAULT '',
    provider              TEXT NOT NULL DEFAULT '',
    provider_call_id      TEXT NOT NULL DEFAULT '',
    direction             TEXT NOT NULL,
    kind                  TEXT NOT NULL,
    status                TEXT NOT NULL,
    started_at            TEXT NOT NULL,
    answered_at           TEXT NOT NULL DEFAULT '',
    ended_at              TEXT NOT NULL DEFAULT '',
    error_message         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_call_legs_call
    ON call_legs(call_id, started_at);

CREATE TABLE IF NOT EXISTS call_offers (
    id              TEXT PRIMARY KEY,
    call_id         TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    project_id      TEXT NOT NULL,
    ring_group_id   TEXT NOT NULL DEFAULT '',
    destination_id  TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'offered',
    offered_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    claimed_at      TEXT NOT NULL DEFAULT '',
    declined_at     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_call_offers_pending
    ON call_offers(project_id, status, expires_at);

ALTER TABLE inbound_routes ADD COLUMN flow_id TEXT NOT NULL DEFAULT '';
ALTER TABLE inbound_routes ADD COLUMN published_flow_version_id TEXT NOT NULL DEFAULT '';

ALTER TABLE calls ADD COLUMN routing_flow_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN routing_flow_version_id TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN routing_destination_id TEXT NOT NULL DEFAULT '';
