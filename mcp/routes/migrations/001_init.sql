-- Apteva Routes v0.1.0 — hostname → target table.
--
-- Owns the public hostname routing for an Apteva install. Apps
-- (deploy, code, …) call routes_register to claim a hostname for
-- their backend; apteva-server reads this table and reverse-proxies
-- inbound traffic accordingly. Manual entries (owner_install_id=0)
-- come from the panel and let users front any local service that
-- isn't owned by an app.

CREATE TABLE host_routes (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  hostname          TEXT    NOT NULL UNIQUE,            -- e.g. blog.example.com
  target            TEXT    NOT NULL,                    -- http://127.0.0.1:7100
  owner_install_id  INTEGER NOT NULL,                    -- 0 = manual (panel-entered)
  owner_kind        TEXT    NOT NULL DEFAULT '',         -- 'deploy' | 'code' | 'manual' | future
  cert_fqdn         TEXT    NOT NULL DEFAULT '',         -- '' = use hostname; otherwise pin a wildcard cert
  allow_http        INTEGER NOT NULL DEFAULT 0,          -- 1 = serve plain HTTP (no 301 to HTTPS)
  created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_host_routes_owner ON host_routes(owner_install_id);
CREATE INDEX ix_host_routes_owner_kind ON host_routes(owner_kind);
