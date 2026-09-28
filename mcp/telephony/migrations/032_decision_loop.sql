ALTER TABLE routing_decisions ADD COLUMN not_before TEXT NOT NULL DEFAULT '';
ALTER TABLE call_offers ADD COLUMN acknowledged_at TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN routing_resolution TEXT NOT NULL DEFAULT '';
ALTER TABLE calls ADD COLUMN callback_on_ai INTEGER NOT NULL DEFAULT 0;

DROP TRIGGER routing_offer_outcome;
CREATE TRIGGER routing_offer_outcome AFTER UPDATE OF status ON call_offers
WHEN NEW.status<>OLD.status AND NEW.status IN ('offered','claimed','failed','expired','canceled','declined')
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at)
 SELECT d.id,'offer.'||NEW.status,NEW.id,NEW.destination_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 WHERE r.id=NEW.run_id AND d.status='accepted';
END;

CREATE TRIGGER routing_offer_acknowledged AFTER UPDATE OF acknowledged_at ON call_offers
WHEN NEW.acknowledged_at<>'' AND OLD.acknowledged_at=''
BEGIN
 INSERT OR IGNORE INTO routing_outcome_marks(decision_id,kind,item_key,destination_id,occurred_at)
 SELECT d.id,'offer.acknowledged',NEW.id,NEW.destination_id,NEW.acknowledged_at
 FROM routing_decisions d JOIN call_ring_runs r ON r.call_id=d.call_id AND r.node_id=d.node_id
 WHERE r.id=NEW.run_id AND d.status='accepted';
END;
