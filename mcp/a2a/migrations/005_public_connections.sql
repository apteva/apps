-- a2a v0.5.0: generic persistent connections and direct public Agent Cards.

ALTER TABLE a2a_peers ADD COLUMN kind TEXT NOT NULL DEFAULT 'node';
ALTER TABLE a2a_peers ADD COLUMN discovery_url TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_peers ADD COLUMN protocol_version TEXT NOT NULL DEFAULT '';
ALTER TABLE a2a_peers ADD COLUMN managed_by TEXT NOT NULL DEFAULT 'config';

UPDATE a2a_peers SET managed_by = 'app' WHERE owner_install_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_a2a_peers_kind
    ON a2a_peers(kind);
