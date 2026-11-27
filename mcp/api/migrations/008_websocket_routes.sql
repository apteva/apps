-- WebSocket pass-through (0.16.0): opt-in per app route, plus one audit
-- record per connection. websocket_json is '{}' when the route does not
-- accept WebSocket upgrades.
ALTER TABLE api_routes ADD COLUMN websocket_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE api_configuration_routes ADD COLUMN websocket_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE api_request_logs ADD COLUMN bytes_in INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_request_logs ADD COLUMN bytes_out INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_request_logs ADD COLUMN close_code INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_request_logs ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE api_request_logs ADD COLUMN opened_at TEXT NOT NULL DEFAULT '';
