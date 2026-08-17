-- Browser softphone support.
--
-- peer_kind names what sits on the far side of the audio bridge:
--   'realtime' — a Core realtime thread (every call before this migration)
--   'human'    — a browser attached to the softphone hub
--
-- Defaulting to 'realtime' keeps every existing row and every code path that
-- does not inspect peer_kind behaving exactly as before.
ALTER TABLE calls ADD COLUMN peer_kind TEXT NOT NULL DEFAULT 'realtime';

-- Per-call secret the browser presents on its media socket. Distinct from
-- callback_secret so a leaked carrier webhook token cannot open an operator
-- audio path, and vice versa.
ALTER TABLE calls ADD COLUMN peer_token TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_calls_peer_kind ON calls(peer_kind, status);
