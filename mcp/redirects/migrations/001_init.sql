-- Apteva Redirects v0.1.0 — (hostname, path) → destination URL.
--
-- Each row is one redirect rule. The runtime handler is a single
-- catch-all on `/` that takes the inbound Host + URL.Path, queries
-- this table, and issues a 30x with a Location header. Multiple rules
-- can target the same hostname (one per path); rule selection is
-- longest-match with `exact` winning over `prefix` on a tie.
--
-- DNS auto-config and ingress wiring don't live here — those are the
-- domains and routes apps. On every insert/update the app calls
-- routes.routes_register to claim the hostname; domains gets a CNAME
-- upsert when the hostname is known to it.

CREATE TABLE redirects (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  hostname        TEXT    NOT NULL,                       -- e.g. go.acme.com
  path            TEXT    NOT NULL DEFAULT '/',           -- '/' for whole host, '/promo' for one link
  match_mode      TEXT    NOT NULL DEFAULT 'exact',       -- 'exact' | 'prefix'
  destination     TEXT    NOT NULL,                       -- full URL, e.g. https://example.com/x
  status_code     INTEGER NOT NULL DEFAULT 302,           -- 301 | 302 | 307 | 308
  preserve_path   INTEGER NOT NULL DEFAULT 0,             -- 1 = append leftover path to destination (prefix only)
  preserve_query  INTEGER NOT NULL DEFAULT 1,             -- 1 = forward inbound ?query string
  project_id      TEXT    NOT NULL DEFAULT '',            -- '' for install-scope, otherwise the owning project
  notes           TEXT    NOT NULL DEFAULT '',
  hits            INTEGER NOT NULL DEFAULT 0,             -- best-effort counter, no per-event log in v0.1
  last_hit_at     DATETIME,
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(hostname, path, match_mode, project_id)
);

CREATE INDEX ix_redirects_hostname ON redirects(hostname);
CREATE INDEX ix_redirects_project  ON redirects(project_id);
