-- Delivery lease for the incoming-call outbox. A sender claims the row with a
-- fresh token before SendEvent and completes only if the token still matches,
-- so inline delivery and the lifecycle tick cannot both send, and a row re-armed
-- for a new agent mid-send is not marked delivered by the old send.
ALTER TABLE inbound_event_outbox ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
