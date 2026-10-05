CREATE TABLE content_preview_links (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 site_id INTEGER NOT NULL REFERENCES sites(id),
 token_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 revoked_at INTEGER,
 created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX content_preview_site_idx ON content_preview_links(project_id,site_id,expires_at);
CREATE UNIQUE INDEX content_preview_token_idx ON content_preview_links(project_id,site_id,token_hash);
