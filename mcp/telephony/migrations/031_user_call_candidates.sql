-- Adviser lists and stream snapshots select through these resource-scoped
-- paths before applying the existing per-call permission checks.
CREATE INDEX IF NOT EXISTS idx_call_owners_destination
    ON telephony_call_owners(project_id, destination_id, call_id);
CREATE INDEX IF NOT EXISTS idx_call_offers_destination_active
    ON call_offers(project_id, destination_id, status, kind, expires_at, call_id);
CREATE INDEX IF NOT EXISTS idx_calls_user_inbound_destination
    ON calls(project_id, routing_destination_id, status, placed_at DESC, id DESC)
    WHERE direction='inbound';
CREATE INDEX IF NOT EXISTS idx_calls_user_outbound_number
    ON calls(project_id, from_number, placed_at DESC, id DESC)
    WHERE direction='outbound';
