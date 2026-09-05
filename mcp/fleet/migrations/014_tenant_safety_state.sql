-- Safety state must outlive audit retention and process-status changes.
CREATE TABLE fleet_tenant_state (
 tenant_id TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 setup_complete INTEGER NOT NULL DEFAULT 0,
 quarantine_required INTEGER NOT NULL DEFAULT 0,
 health_failures INTEGER NOT NULL DEFAULT 0,
 dns_project_id TEXT NOT NULL DEFAULT '',
 dns_epoch INTEGER NOT NULL DEFAULT 0,
 setup_password_enc BLOB,
 setup_phase TEXT NOT NULL DEFAULT ''
);
INSERT INTO fleet_tenant_state (tenant_id, setup_complete, quarantine_required)
 SELECT id, CASE WHEN status IN ('starting','setup_pending') OR setup_token_enc IS NOT NULL THEN 0 ELSE 1 END,
 CASE WHEN EXISTS(SELECT 1 FROM fleet_events e WHERE e.tenant_id=t.id AND e.kind='cloned')
 AND NOT EXISTS(SELECT 1 FROM fleet_events e WHERE e.tenant_id=t.id AND e.kind='clone_rehearsal_validated') THEN 1 ELSE 0 END
 FROM fleet_tenants t;
CREATE TRIGGER fleet_tenant_state_insert AFTER INSERT ON fleet_tenants BEGIN
 INSERT INTO fleet_tenant_state(tenant_id,setup_complete)
 VALUES(NEW.id, CASE WHEN NEW.status IN ('starting','setup_pending') OR NEW.setup_token_enc IS NOT NULL THEN 0 ELSE 1 END);
END;
CREATE TRIGGER fleet_clone_state_event AFTER INSERT ON fleet_events
 WHEN NEW.kind IN ('cloned','clone_rehearsal_validated') BEGIN
 UPDATE fleet_tenant_state SET quarantine_required=CASE WHEN NEW.kind='cloned' THEN 1 ELSE 0 END WHERE tenant_id=NEW.tenant_id;
END;
CREATE TABLE fleet_active_operations (
 tenant_id TEXT PRIMARY KEY REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 operation TEXT NOT NULL,
 phase TEXT NOT NULL DEFAULT 'running',
 snapshot TEXT NOT NULL DEFAULT '{}',
 started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE fleet_port_reservations (
 instance_id INTEGER NOT NULL,
 port INTEGER NOT NULL,
 tenant_id TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 purpose TEXT NOT NULL,
 PRIMARY KEY(instance_id,port)
);
CREATE TABLE fleet_hostname_owners (
 hostname TEXT NOT NULL,
 tenant_id TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 wildcard INTEGER NOT NULL DEFAULT 0,
 purpose TEXT NOT NULL,
 PRIMARY KEY(hostname,purpose)
);

CREATE TABLE fleet_app_port_blocks (
 instance_id INTEGER NOT NULL,
 base INTEGER NOT NULL,
 tenant_id TEXT NOT NULL REFERENCES fleet_tenants(id) ON DELETE CASCADE,
 PRIMARY KEY(instance_id,base), UNIQUE(tenant_id,instance_id)
);

-- SQLite serializes these checks with the insert, including across controllers.
CREATE TRIGGER fleet_hostname_owner_conflict BEFORE INSERT ON fleet_hostname_owners
WHEN EXISTS (
 SELECT 1 FROM (
  SELECT tenant_id,lower(rtrim(hostname,'.')) AS name,wildcard FROM fleet_hostname_owners
  UNION ALL SELECT id,lower(rtrim(domain,'.')),0 FROM fleet_tenants WHERE domain IS NOT NULL
  UNION ALL SELECT tenant_id,lower(rtrim(hostname,'.')),0 FROM fleet_tenant_hosts
  UNION ALL SELECT tenant_id,lower(rtrim(domain,'.')),wildcard FROM fleet_domain_grants
 ) WHERE tenant_id!=NEW.tenant_id AND
 (name=NEW.hostname OR (wildcard=1 AND substr(NEW.hostname,-length(name)-1)='.'||name)
 OR (NEW.wildcard=1 AND substr(name,-length(NEW.hostname)-1)='.'||NEW.hostname))
) BEGIN SELECT RAISE(ABORT,'hostname or delegated zone already owned'); END;
CREATE TRIGGER fleet_management_range_conflict BEFORE INSERT ON fleet_port_reservations
WHEN EXISTS(SELECT 1 FROM fleet_app_port_blocks WHERE instance_id=NEW.instance_id
 AND NEW.port BETWEEN base AND base+999)
BEGIN SELECT RAISE(ABORT,'management port overlaps application range'); END;
CREATE TRIGGER fleet_application_range_conflict BEFORE INSERT ON fleet_app_port_blocks
WHEN EXISTS(SELECT 1 FROM fleet_port_reservations WHERE instance_id=NEW.instance_id
 AND port BETWEEN NEW.base AND NEW.base+999)
BEGIN SELECT RAISE(ABORT,'application range overlaps management port'); END;
CREATE INDEX fleet_tenants_list_order ON fleet_tenants(created_at DESC,id DESC);
