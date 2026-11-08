-- Counters are admitted reservations, persisted before protocol side effects.
-- period_start=0 is lifetime; one persisted fixed window is retained per scope.
CREATE TABLE mqtt_usage_counters (
 scope_id TEXT NOT NULL, period_start INTEGER NOT NULL, metric TEXT NOT NULL,
 value INTEGER NOT NULL CHECK(typeof(value)='integer' AND value>=0),
 PRIMARY KEY(scope_id,period_start,metric)
);
CREATE TABLE mqtt_usage_windows(scope_id TEXT PRIMARY KEY, starts_at INTEGER NOT NULL, ends_at INTEGER NOT NULL);
CREATE TABLE mqtt_usage_connections(client_id TEXT PRIMARY KEY, scope_id TEXT NOT NULL, token TEXT NOT NULL UNIQUE);
CREATE INDEX mqtt_usage_connections_scope ON mqtt_usage_connections(scope_id);
CREATE TABLE mqtt_quota_policies(scope_id TEXT PRIMARY KEY, policy TEXT NOT NULL);
CREATE TABLE mqtt_usage_state(name TEXT PRIMARY KEY,value INTEGER NOT NULL);
INSERT INTO mqtt_usage_state VALUES('started_at',unixepoch());
CREATE TABLE mqtt_retained_usage(scope_id TEXT PRIMARY KEY,messages INTEGER NOT NULL,bytes INTEGER NOT NULL);
INSERT INTO mqtt_retained_usage SELECT scope_id,count(*),sum(length(CAST(topic AS BLOB))+length(payload)+length(CAST(properties AS BLOB))) FROM mqtt_retained WHERE scope_id!='' GROUP BY scope_id;
CREATE TRIGGER mqtt_retained_usage_insert AFTER INSERT ON mqtt_retained WHEN new.scope_id!='' BEGIN
 INSERT INTO mqtt_retained_usage VALUES(new.scope_id,1,length(CAST(new.topic AS BLOB))+length(new.payload)+length(CAST(new.properties AS BLOB)))
 ON CONFLICT(scope_id) DO UPDATE SET messages=messages+1,bytes=bytes+excluded.bytes;
END;
CREATE TRIGGER mqtt_retained_usage_delete AFTER DELETE ON mqtt_retained WHEN old.scope_id!='' BEGIN
 UPDATE mqtt_retained_usage SET messages=messages-1,bytes=bytes-length(CAST(old.topic AS BLOB))-length(old.payload)-length(CAST(old.properties AS BLOB)) WHERE scope_id=old.scope_id;
END;
CREATE TRIGGER mqtt_retained_usage_update AFTER UPDATE OF scope_id,topic,payload,properties ON mqtt_retained BEGIN
 UPDATE mqtt_retained_usage SET messages=messages-1,bytes=bytes-length(CAST(old.topic AS BLOB))-length(old.payload)-length(CAST(old.properties AS BLOB)) WHERE scope_id=old.scope_id;
 INSERT INTO mqtt_retained_usage SELECT new.scope_id,1,length(CAST(new.topic AS BLOB))+length(new.payload)+length(CAST(new.properties AS BLOB)) WHERE new.scope_id!=''
 ON CONFLICT(scope_id) DO UPDATE SET messages=messages+1,bytes=bytes+excluded.bytes;
END;
