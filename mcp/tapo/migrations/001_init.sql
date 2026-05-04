-- tapo v0.1: Tapo camera registry + motion event cache.
--
-- Cameras are project-scoped (so the same physical device can be
-- registered in two scopes if e.g. one team owns the porch cam and a
-- separate kid-monitoring instance also wants to see it). The id is
-- per-row, the apteva project_id matches the storage/todo apps'
-- convention.
--
-- Credentials: password is stored as either:
--   * raw plaintext (when the install has no APTEVA_SECRET set) — the
--     app DB is private to this install, but on shared infra you
--     should set the shared_secret config and re-add cameras.
--   * `enc::<base64-of-aes-gcm-ciphertext>` when shared_secret is set.
-- The app handles both transparently — see decryptPassword in main.go.
--
-- capabilities_json is filled by the probe step and is the source of
-- truth for whether to register PTZ tools, sirens, etc. Re-probed by
-- cameras_test or whenever the camera comes back online after being
-- offline > 1h.

CREATE TABLE IF NOT EXISTS cameras (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          TEXT    NOT NULL,
    name                TEXT    NOT NULL,
    room                TEXT    NOT NULL DEFAULT '',
    ip                  TEXT    NOT NULL,
    username            TEXT    NOT NULL,
    password_enc        TEXT    NOT NULL,             -- raw or 'enc::...'
    model               TEXT    NOT NULL DEFAULT '',  -- e.g. C200, C220
    firmware            TEXT    NOT NULL DEFAULT '',
    capabilities_json   TEXT    NOT NULL DEFAULT '{}',
    online              INTEGER NOT NULL DEFAULT 0,
    last_seen_at        TEXT,                         -- RFC3339
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_cameras_scope ON cameras(project_id);
CREATE INDEX IF NOT EXISTS idx_cameras_ip    ON cameras(ip);

-- Rolling motion event cache. Pruned by a worker on a fixed schedule
-- (motion_event_retention_days from config). The on-camera event log
-- is the system of record; this table is just for quick listing in
-- the panel and for cross-app emission.
CREATE TABLE IF NOT EXISTS motion_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    camera_id           INTEGER NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    project_id          TEXT    NOT NULL,
    occurred_at         TEXT    NOT NULL,             -- RFC3339 UTC
    kind                TEXT    NOT NULL DEFAULT 'motion'
                        CHECK(kind IN ('motion','person','pet','baby_cry','sound')),
    bbox_json           TEXT    NOT NULL DEFAULT '',
    snapshot_file_id    INTEGER,                      -- ref into storage app
    raw_event_id        TEXT    NOT NULL DEFAULT '',  -- camera's own id, for dedupe
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(camera_id, raw_event_id)
);

CREATE INDEX IF NOT EXISTS idx_events_scope_time ON motion_events(project_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_camera_time ON motion_events(camera_id, occurred_at DESC);
