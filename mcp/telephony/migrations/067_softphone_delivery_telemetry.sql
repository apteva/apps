CREATE TABLE telephony_offer_browser_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 offer_id TEXT NOT NULL,
 principal TEXT NOT NULL,
 browser_tab_id TEXT NOT NULL,
 browser_session_id TEXT NOT NULL,
 event_id TEXT NOT NULL,
 stage TEXT NOT NULL,
 client_occurred_at TEXT NOT NULL DEFAULT '',
 received_at TEXT NOT NULL,
 request_id TEXT NOT NULL DEFAULT '',
 facts_json TEXT NOT NULL DEFAULT '{}',
 expires_at TEXT NOT NULL,
 UNIQUE(project_id,principal,browser_session_id,event_id)
);
CREATE INDEX idx_offer_browser_call ON telephony_offer_browser_events(project_id,call_id,id);
CREATE INDEX idx_offer_browser_offer ON telephony_offer_browser_events(offer_id,principal,browser_session_id);
CREATE INDEX idx_offer_browser_retention ON telephony_offer_browser_events(expires_at,id);
CREATE INDEX idx_offer_browser_call_fk ON telephony_offer_browser_events(call_id);
CREATE TABLE telephony_browser_watch_health (
 project_id TEXT NOT NULL,
 principal TEXT NOT NULL,
 browser_tab_id TEXT NOT NULL,
 browser_session_id TEXT NOT NULL,
 watch_id TEXT NOT NULL,
 snapshot_json TEXT NOT NULL,
 received_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 PRIMARY KEY(project_id,principal,browser_session_id,watch_id)
);
CREATE INDEX idx_browser_watch_health_retention ON telephony_browser_watch_health(expires_at);
