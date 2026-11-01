-- Saved layouts: reusable signer roles + field placement, applied to a
-- new envelope so recurring documents (model releases, NDAs) need no
-- manual field placement. Recipients and fields are JSON because a
-- layout is only ever read and written whole.
CREATE TABLE IF NOT EXISTS layouts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    slug        TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    recipients  TEXT    NOT NULL,
    fields      TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    UNIQUE(project_id, slug)
);
