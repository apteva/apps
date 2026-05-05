-- Live Link v0.2 — named (stable-URL) tunnels.
--
-- A row here represents the persistent CF-side state for a stable
-- hostname: the tunnel UUID, the connector token, and the DNS record
-- we manage. There is at most one row per install in v0.2 (one
-- hostname per install), keyed by hostname for idempotent upserts.
--
-- The connector token is sensitive. It's stored in the app's own
-- SQLite DB inside the install's data dir, which the platform already
-- treats as secret-grade. Don't surface it through any read endpoint.

CREATE TABLE named_tunnels (
  id              INTEGER PRIMARY KEY,
  hostname        TEXT    NOT NULL UNIQUE,           -- e.g. tunnel.example.com
  tunnel_id       TEXT    NOT NULL,                  -- CF tunnel UUID
  tunnel_token    TEXT    NOT NULL,                  -- connector token (run --token)
  account_id      TEXT    NOT NULL,                  -- CF account that owns the tunnel
  zone_id         TEXT    NOT NULL,                  -- CF zone hosting the CNAME
  dns_record_id   TEXT    NOT NULL,                  -- record id for cleanup on destroy
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Tag every run with the mode it ran in so history can show "stable"
-- vs "ephemeral" runs at a glance. Default 'quick' so existing rows
-- (and Quick Tunnel runs going forward) keep working unchanged.
ALTER TABLE runs ADD COLUMN mode TEXT NOT NULL DEFAULT 'quick';
