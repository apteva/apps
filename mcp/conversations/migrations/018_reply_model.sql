-- The provider and model whose call produced an agent message, as Core's
-- telemetry reported it ({"provider":"…","model":"…"}). Kept out of
-- metadata_json so message payloads (including public visitors') never
-- carry it; operators read it through /participants.
ALTER TABLE messages ADD COLUMN generated_by TEXT NOT NULL DEFAULT '';
