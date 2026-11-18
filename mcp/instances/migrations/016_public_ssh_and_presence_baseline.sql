-- Public SSH for reverse-tunnel machines: Instances proxies public_port on
-- this server to the tunnel's loopback port. 0 means not exposed.
ALTER TABLE reverse_tunnels ADD COLUMN public_port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reverse_tunnels ADD COLUMN public_cidrs TEXT NOT NULL DEFAULT '';
ALTER TABLE reverse_tunnels ADD COLUMN public_max_connections INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reverse_tunnels ADD COLUMN public_firewall TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS reverse_tunnels_public_port ON reverse_tunnels(public_port) WHERE public_port > 0;

-- Rows that were never checked record their first state without an event, so
-- an upgrade does not announce every reachable machine as newly connected.
ALTER TABLE instances ADD COLUMN presence_known INTEGER NOT NULL DEFAULT 1;
UPDATE instances SET presence_known = 0 WHERE last_seen_at = '';

-- Identifies this install in files it shares with other software on the
-- server, such as the tunnel account's authorized_keys.
CREATE TABLE IF NOT EXISTS install_identity (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Fingerprint the operator expects for a machine's SSH host key, from the
-- registration command's output. Checked instead of trusting the first key.
ALTER TABLE instances ADD COLUMN ssh_host_key_expected TEXT NOT NULL DEFAULT '';
