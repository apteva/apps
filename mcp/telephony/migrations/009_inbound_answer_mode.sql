ALTER TABLE inbound_routes ADD COLUMN answer_mode TEXT NOT NULL DEFAULT 'agent';
ALTER TABLE inbound_routes ADD COLUMN auto_directive TEXT NOT NULL DEFAULT '';
ALTER TABLE inbound_routes ADD COLUMN auto_voice TEXT NOT NULL DEFAULT '';
ALTER TABLE inbound_routes ADD COLUMN auto_greeting TEXT NOT NULL DEFAULT '';
