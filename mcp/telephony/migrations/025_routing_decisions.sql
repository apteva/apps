CREATE TABLE routing_decisions (
 id TEXT PRIMARY KEY,
 call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 flow_version_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 request_json TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '{}',
 reason TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 deadline_at TEXT NOT NULL,
 completed_at TEXT NOT NULL DEFAULT '',
 applied INTEGER NOT NULL DEFAULT 0,
 UNIQUE(call_id,node_id)
);
CREATE INDEX routing_decisions_pending ON routing_decisions(project_id,status,applied);
CREATE TABLE phone_capacity (
 call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL,
 destination_id TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(call_id,principal)
);
CREATE INDEX phone_capacity_principal ON phone_capacity(project_id,principal);
ALTER TABLE routing_decisions ADD COLUMN plan_json TEXT NOT NULL DEFAULT 'null';

-- Preserve historical events while allowing repeated routing topics per call.
ALTER TABLE call_events RENAME TO call_events_legacy;
CREATE TABLE call_events (
 event_id TEXT PRIMARY KEY, call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, topic TEXT NOT NULL, revision INTEGER NOT NULL,
 occurred_at TEXT NOT NULL, payload_json TEXT NOT NULL, created_at TEXT NOT NULL,
 published_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO call_events SELECT * FROM call_events_legacy;
DROP TABLE call_events_legacy;
CREATE INDEX idx_call_events_publish ON call_events(project_id,published_at,created_at);
CREATE INDEX idx_call_events_call_revision ON call_events(call_id,revision);

-- Journal transitions in their original transaction, even when setup fails or
-- completes between worker ticks. Conversion to the existing outbox is retryable.
CREATE TABLE routing_outcome_marks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 decision_id TEXT NOT NULL REFERENCES routing_decisions(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, item_key TEXT NOT NULL, destination_id TEXT NOT NULL DEFAULT '',
 occurred_at TEXT NOT NULL, identity_json TEXT NOT NULL DEFAULT '', published INTEGER NOT NULL DEFAULT 0,
 UNIQUE(decision_id,kind,item_key)
);
CREATE TRIGGER routing_offer_outcome AFTER UPDATE OF status ON call_offers
WHEN NEW.status<>OLD.status AND NEW.status IN ('offered','claimed','failed','expired','canceled')
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at)
 SELECT d.id,'offer.'||NEW.status,NEW.id,NEW.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 WHERE r.id=NEW.run_id AND d.status='accepted';
END;
CREATE TRIGGER routing_answerer_outcome AFTER INSERT ON telephony_call_owners
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at,identity_json)
 SELECT d.id,'offer.answerer',o.id,o.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),NEW.principal
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 JOIN call_offers o ON o.run_id=r.id AND o.status='claimed'
 WHERE d.call_id=NEW.call_id AND d.status='accepted';
END;
CREATE TRIGGER routing_connected_outcome AFTER UPDATE OF media_connected_at ON calls
WHEN NEW.media_connected_at<>'' AND OLD.media_connected_at=''
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at,identity_json)
 SELECT d.id,'destination.connected',o.id,o.destination_id,NEW.media_connected_at,
 COALESCE((SELECT principal FROM telephony_call_owners WHERE call_id=NEW.id),'')
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 JOIN call_offers o ON o.run_id=r.id AND o.status='claimed'
 WHERE d.call_id=NEW.id AND d.status='accepted';
END;
CREATE TRIGGER routing_completed_outcome AFTER UPDATE OF status ON calls
WHEN NEW.status<>OLD.status AND NEW.status IN ('completed','failed','busy','no-answer','canceled')
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at,identity_json)
 SELECT d.id,'call.'||NEW.status,NEW.id,NEW.routing_destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),
 COALESCE((SELECT principal FROM telephony_call_owners WHERE call_id=NEW.id),'')
 FROM routing_decisions d WHERE d.call_id=NEW.id;
 DELETE FROM phone_capacity WHERE call_id=NEW.id;
END;
CREATE TRIGGER routing_failed_setup_capacity AFTER UPDATE OF status ON calls
WHEN NEW.status='pending' AND OLD.status='answering'
BEGIN
 DELETE FROM phone_capacity WHERE call_id=NEW.id;
 DELETE FROM telephony_call_owners WHERE call_id=NEW.id;
END;
CREATE TRIGGER routing_connection_failed_outcome AFTER UPDATE OF status ON calls
WHEN NEW.status<>OLD.status AND NEW.status IN ('completed','failed','busy','no-answer','canceled') AND NEW.media_connected_at=''
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at,identity_json)
 SELECT d.id,'destination.connection_failed',o.id,o.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'),
 COALESCE((SELECT principal FROM telephony_call_owners WHERE call_id=NEW.id),'')
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 JOIN call_offers o ON o.run_id=r.id AND o.status='claimed'
 WHERE d.call_id=NEW.id AND d.status='accepted';
END;

ALTER TABLE call_offers ADD COLUMN capacity_principal TEXT NOT NULL DEFAULT '';
