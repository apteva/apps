-- Apteva CDN v0.1 — zones table.
--
-- A zone is one (hostname → origin URL) mapping. Creating a zone
-- writes DNS via the domains app, issues a TLS cert via certs, and
-- registers the host route via routes. apteva-server's HostRouter
-- does the actual reverse-proxy at request time.

CREATE TABLE zones (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id      TEXT    NOT NULL,
  hostname        TEXT    NOT NULL,
  origin_url      TEXT    NOT NULL,           -- http(s)://host[:port][/path]
  record_type     TEXT    NOT NULL DEFAULT 'A',
  record_value    TEXT    NOT NULL DEFAULT '',-- snapshot of server_public_host at create time; '' when skip_dns
  allow_http      INTEGER NOT NULL DEFAULT 0, -- 1 = serve HTTP without HTTPS redirect; cert leg skipped
  status          TEXT    NOT NULL DEFAULT 'pending', -- pending | active | error
  status_detail   TEXT    NOT NULL DEFAULT '',
  dns_status      TEXT    NOT NULL DEFAULT '', -- ok | error | skipped
  cert_status     TEXT    NOT NULL DEFAULT '', -- ok | error | skipped
  route_status    TEXT    NOT NULL DEFAULT '', -- ok | error | skipped
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (project_id, hostname)
);

CREATE INDEX ix_zones_project ON zones(project_id);
CREATE INDEX ix_zones_hostname ON zones(hostname);
