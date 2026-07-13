-- A public hostname is globally unique at the platform ingress layer, so it
-- must belong to exactly one project inside a global Redirects install too.
CREATE TABLE redirect_hosts (
  hostname    TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL DEFAULT '',
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Existing installs may predate hostname ownership. Keep the oldest claim
-- deterministically; runtime matching uses this table, so legacy duplicates
-- can no longer resolve to an arbitrary project's destination.
INSERT OR IGNORE INTO redirect_hosts (hostname, project_id)
SELECT lower(rtrim(trim(hostname), '.')), project_id
FROM redirects
ORDER BY id ASC;
