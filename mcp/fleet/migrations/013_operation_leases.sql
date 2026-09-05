-- fleet 013: keep hosted update exclusion durable across Fleet restarts.
-- A lease is deliberately not time-based: after an interrupted remote stop,
-- automatically guessing that it expired could start a second tenant runtime.

CREATE TABLE IF NOT EXISTS fleet_operation_leases (
    tenant_id               TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
    operation               TEXT NOT NULL,
    phase                   TEXT NOT NULL,
    requested_version       TEXT NOT NULL DEFAULT '',
    previous_version        TEXT NOT NULL DEFAULT '',
    previous_target_version TEXT NOT NULL DEFAULT '',
    updated_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
