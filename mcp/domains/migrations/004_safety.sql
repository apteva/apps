-- Old null pins cannot distinguish intentionally external domains from legacy
-- defaults. Require an explicit connection selection for those rows.
ALTER TABLE domains ADD COLUMN connection_mode TEXT NOT NULL DEFAULT 'unmanaged';
UPDATE domains SET connection_mode='pinned' WHERE connection_id > 0;

ALTER TABLE registration_intents ADD COLUMN cost_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE registration_intents ADD COLUMN result_json TEXT NOT NULL DEFAULT '';
ALTER TABLE registration_intents ADD COLUMN attempted_at TEXT NOT NULL DEFAULT '';
-- Old prepared intents did not contain a validated payable quote.
UPDATE registration_intents SET status='expired' WHERE status='prepared';
UPDATE registration_intents SET status='unknown' WHERE status='processing';

CREATE TABLE dns_recoveries (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  connection_id INTEGER NOT NULL,
  domain TEXT NOT NULL,
  previous_json TEXT NOT NULL,
  desired_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  error_message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_dns_recoveries_project ON dns_recoveries(project_id,status);

-- Serialize conflicting purchases across processes as well as goroutines.
-- Existing unresolved legacy rows are retained for investigation.
CREATE TRIGGER registration_one_pending_update BEFORE UPDATE OF status ON registration_intents
WHEN NEW.status IN ('processing','unknown') AND EXISTS (
 SELECT 1 FROM registration_intents WHERE project_id=NEW.project_id AND domain=NEW.domain
 AND token<>NEW.token AND status IN ('processing','unknown')
)
BEGIN SELECT RAISE(ABORT,'another purchase for this domain is unresolved'); END;
CREATE TRIGGER registration_one_pending_insert BEFORE INSERT ON registration_intents
WHEN NEW.status IN ('prepared','processing','unknown') AND EXISTS (
 SELECT 1 FROM registration_intents WHERE project_id=NEW.project_id AND domain=NEW.domain
 AND status IN ('processing','unknown')
)
BEGIN SELECT RAISE(ABORT,'another purchase for this domain is unresolved'); END;
