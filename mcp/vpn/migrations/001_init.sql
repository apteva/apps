-- One row per project_id. A given install is normally bound to one
-- project, so this is effectively a singleton — but keying on
-- project_id (not a CHECK(id=1)) keeps the door open for any future
-- mode where one install handles many scopes.
CREATE TABLE server (
    project_id   TEXT    PRIMARY KEY,
    host_id      INTEGER NOT NULL,
    backend      TEXT    NOT NULL DEFAULT 'wireguard',
    public_key   TEXT    NOT NULL,
    private_key  TEXT    NOT NULL,
    endpoint     TEXT    NOT NULL,
    listen_port  INTEGER NOT NULL,
    network_cidr TEXT    NOT NULL,
    installed_at INTEGER NOT NULL,
    last_poll_at INTEGER,
    last_poll_ok INTEGER
);

CREATE TABLE peer (
    id                INTEGER PRIMARY KEY,
    project_id        TEXT    NOT NULL,
    name              TEXT    NOT NULL,
    public_key        TEXT    NOT NULL,
    private_key       TEXT    NOT NULL,
    preshared_key     TEXT    NOT NULL,
    address           TEXT    NOT NULL,
    allowed_ips       TEXT    NOT NULL,
    dns               TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    revoked_at        INTEGER,
    last_handshake_at INTEGER,
    rx_bytes          INTEGER NOT NULL DEFAULT 0,
    tx_bytes          INTEGER NOT NULL DEFAULT 0,
    UNIQUE(project_id, name),
    UNIQUE(project_id, public_key),
    UNIQUE(project_id, address)
);

CREATE INDEX peer_active_idx ON peer(project_id, revoked_at);
