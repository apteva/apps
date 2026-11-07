-- Rebuild only deliveries to extend its type constraint. Devices, encrypted
-- tokens, grants, IDs, timestamps and idempotency receipts remain unchanged.
CREATE TABLE deliveries_with_notifications (
  id TEXT PRIMARY KEY,
  grant_id TEXT NOT NULL REFERENCES grants(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  idempotency_key TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('notification', 'approval', 'alert', 'report', 'test')),
  item_id TEXT NOT NULL DEFAULT '',
  badge INTEGER,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  provider_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  sent_at TEXT,
  project_id TEXT NOT NULL DEFAULT '',
  UNIQUE(grant_id, idempotency_key)
);

INSERT INTO deliveries_with_notifications
  (id, grant_id, device_id, idempotency_key, type, item_id, badge, status,
   provider_id, error, created_at, sent_at, project_id)
SELECT id, grant_id, device_id, idempotency_key, type, item_id, badge, status,
       provider_id, error, created_at, sent_at, project_id
FROM deliveries;

DROP TABLE deliveries;
ALTER TABLE deliveries_with_notifications RENAME TO deliveries;

CREATE INDEX idx_deliveries_device_created ON deliveries(device_id, created_at DESC);
CREATE INDEX idx_deliveries_status_created ON deliveries(status, created_at DESC);
CREATE INDEX idx_deliveries_project_created ON deliveries(project_id, created_at DESC);
