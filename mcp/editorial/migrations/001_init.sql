CREATE TABLE editorial_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 data TEXT NOT NULL CHECK(json_valid(data)),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(project_id,id)
);
CREATE INDEX editorial_items_project ON editorial_items(project_id, id);
CREATE TABLE editorial_releases (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 item_id INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1,
 data TEXT NOT NULL CHECK(json_valid(data)),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 FOREIGN KEY(project_id,item_id) REFERENCES editorial_items(project_id,id)
);
CREATE INDEX editorial_releases_parent ON editorial_releases(project_id,item_id);
CREATE TABLE editorial_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 item_id INTEGER NOT NULL,
 action TEXT NOT NULL,
 snapshot TEXT NOT NULL CHECK(json_valid(snapshot)),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 FOREIGN KEY(project_id,item_id) REFERENCES editorial_items(project_id,id)
);
CREATE INDEX editorial_history_parent ON editorial_history(project_id,item_id,id);
CREATE TABLE editorial_settings (
 project_id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 data TEXT NOT NULL CHECK(json_valid(data))
);
