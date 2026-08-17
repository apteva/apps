ALTER TABLE inbound_routes ADD COLUMN inbound_transport TEXT NOT NULL DEFAULT 'programmable_websocket';
ALTER TABLE inbound_routes ADD COLUMN transport_config TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_inbound_routes_direct_sip
    ON inbound_routes(inbound_transport, phone_number, enabled);
