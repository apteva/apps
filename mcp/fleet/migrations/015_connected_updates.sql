CREATE TABLE fleet_connected_updates (
 tenant_id TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 grant_enc BLOB NOT NULL,
 request_id TEXT NOT NULL DEFAULT '',
 version TEXT NOT NULL DEFAULT '',
 agent_policy TEXT NOT NULL DEFAULT '',
 actor TEXT NOT NULL DEFAULT '',
 job_id TEXT NOT NULL DEFAULT '',
 phase TEXT NOT NULL DEFAULT '',
 notice TEXT NOT NULL DEFAULT '',
 requested_at INTEGER NOT NULL DEFAULT 0,
 attempted_at INTEGER NOT NULL DEFAULT 0,
 done INTEGER NOT NULL DEFAULT 1
);

CREATE TRIGGER fleet_prevent_disconnect_during_update BEFORE DELETE ON fleet_tenants
WHEN EXISTS (SELECT 1 FROM fleet_connected_updates WHERE tenant_id=OLD.id AND done=0)
BEGIN
 SELECT RAISE(ABORT, 'connected tenant has a pending update');
END;
