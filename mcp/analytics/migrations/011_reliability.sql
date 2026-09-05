-- Stable target identities: retiring a target retains its ID permanently.
ALTER TABLE objective_targets ADD COLUMN retired_at INTEGER;
CREATE TABLE dashboard_target_links (
 widget_id INTEGER NOT NULL REFERENCES dashboard_widgets(id) ON DELETE CASCADE,
 target_id INTEGER NOT NULL REFERENCES objective_targets(id),
 PRIMARY KEY(widget_id,target_id)
);
INSERT OR IGNORE INTO dashboard_target_links
 SELECT w.id, t.id FROM dashboard_widgets w, json_each(w.config_json, '$.objective_target_ids') j
 JOIN objective_targets t ON t.id=j.value JOIN objectives o ON o.id=t.objective_id
 JOIN dashboards d ON d.id=w.dashboard_id AND d.project_id=o.project_id;
CREATE TRIGGER dashboard_links_insert AFTER INSERT ON dashboard_widgets BEGIN
 INSERT INTO dashboard_target_links SELECT NEW.id,t.id FROM json_each(NEW.config_json,'$.objective_target_ids') j
 JOIN objective_targets t ON t.id=j.value JOIN objectives o ON o.id=t.objective_id
 JOIN dashboards d ON d.id=NEW.dashboard_id AND d.project_id=o.project_id;
END;
CREATE TRIGGER dashboard_links_update AFTER UPDATE OF config_json ON dashboard_widgets BEGIN
 DELETE FROM dashboard_target_links WHERE widget_id=NEW.id;
 INSERT INTO dashboard_target_links SELECT NEW.id,t.id FROM json_each(NEW.config_json,'$.objective_target_ids') j
 JOIN objective_targets t ON t.id=j.value JOIN objectives o ON o.id=t.objective_id
 JOIN dashboards d ON d.id=NEW.dashboard_id AND d.project_id=o.project_id;
END;
CREATE TABLE ingest_receipts (
 project_id TEXT NOT NULL, app TEXT NOT NULL, topic TEXT NOT NULL, delivery_id TEXT NOT NULL,
 event_id INTEGER NOT NULL, fingerprint TEXT NOT NULL, created_at INTEGER NOT NULL,
 PRIMARY KEY(project_id,app,topic,delivery_id)
);
CREATE TABLE capture_state (id INTEGER PRIMARY KEY CHECK(id=1), seq INTEGER NOT NULL DEFAULT 0, epoch TEXT NOT NULL DEFAULT '', gaps INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO capture_state(id) VALUES(1);
CREATE TABLE capture_inbox (identity TEXT PRIMARY KEY, payload TEXT NOT NULL, received_at INTEGER NOT NULL, processed_at INTEGER);
CREATE INDEX ix_capture_pending ON capture_inbox(processed_at,received_at);
CREATE TABLE capture_projects (project_id TEXT PRIMARY KEY, config TEXT NOT NULL CHECK(json_valid(config)));
-- Copy the old policy only to projects known to this install. New projects opt in.
INSERT INTO capture_projects SELECT DISTINCT project_id,json_object('enabled',json(CASE c.enabled WHEN 1 THEN 'true' ELSE 'false' END),'mode',c.mode,'topic_patterns',json(c.topic_patterns),'sample_rate',c.sample_rate)
 FROM events CROSS JOIN capture_config c WHERE project_id IS NOT NULL AND project_id!='';
CREATE TABLE retention_policy (project_id TEXT PRIMARY KEY, event_days INTEGER NOT NULL DEFAULT 0 CHECK(event_days>=0), diagnostic_days INTEGER NOT NULL DEFAULT 30 CHECK(diagnostic_days>0), archive_days INTEGER NOT NULL DEFAULT 30 CHECK(archive_days>0));
CREATE TABLE event_archive (id INTEGER PRIMARY KEY, project_id TEXT NOT NULL, payload TEXT NOT NULL, archived_at INTEGER NOT NULL);
CREATE INDEX ix_archive_project ON event_archive(project_id,archived_at);
CREATE INDEX ix_violations_seen ON event_spec_violations(seen_at);
