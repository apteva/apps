CREATE TABLE IF NOT EXISTS commons_profiles (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    username        TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    domain          TEXT NOT NULL,
    actor_url       TEXT NOT NULL UNIQUE,
    inbox_url       TEXT NOT NULL,
    outbox_url      TEXT NOT NULL,
    public_key_pem  TEXT NOT NULL,
    private_key_pem TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS commons_posts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id    INTEGER NOT NULL REFERENCES commons_profiles(id) ON DELETE CASCADE,
    body          TEXT NOT NULL,
    visibility    TEXT NOT NULL DEFAULT 'public' CHECK(visibility IN ('public')),
    activity_id   TEXT NOT NULL UNIQUE,
    object_id     TEXT NOT NULL UNIQUE,
    activity_json TEXT NOT NULL,
    object_json   TEXT NOT NULL,
    published_at  DATETIME NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS commons_follows (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id   INTEGER NOT NULL REFERENCES commons_profiles(id) ON DELETE CASCADE,
    remote_actor TEXT NOT NULL,
    remote_inbox TEXT NOT NULL,
    remote_name  TEXT NOT NULL DEFAULT '',
    accepted     INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(profile_id, remote_actor)
);

CREATE TABLE IF NOT EXISTS commons_blocks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id INTEGER REFERENCES commons_profiles(id) ON DELETE CASCADE,
    target     TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK(kind IN ('actor','domain')),
    reason     TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(profile_id, target, kind)
);

CREATE TABLE IF NOT EXISTS commons_inbox_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id   INTEGER REFERENCES commons_profiles(id) ON DELETE SET NULL,
    remote_actor TEXT NOT NULL DEFAULT '',
    activity_id  TEXT NOT NULL DEFAULT '',
    activity_type TEXT NOT NULL DEFAULT '',
    raw_json     TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'stored',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS commons_deliveries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id    INTEGER NOT NULL REFERENCES commons_profiles(id) ON DELETE CASCADE,
    target_inbox  TEXT NOT NULL,
    activity_id   TEXT NOT NULL,
    payload_json  TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sent','failed')),
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commons_posts_profile_created ON commons_posts(profile_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commons_deliveries_pending ON commons_deliveries(status, next_attempt_at);
