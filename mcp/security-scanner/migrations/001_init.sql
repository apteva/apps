-- Security Scanner v0.1: allowlist, scans, hosts, services, findings.

CREATE TABLE IF NOT EXISTS targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('allow','exclude')),
    value       TEXT    NOT NULL,
    note        TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    UNIQUE(project_id, kind, value)
);

CREATE TABLE IF NOT EXISTS settings (
    project_id      TEXT    PRIMARY KEY,
    rate_pps        INTEGER NOT NULL DEFAULT 100,
    parallel_hosts  INTEGER NOT NULL DEFAULT 10,
    max_addresses   INTEGER NOT NULL DEFAULT 1024,
    cve_lookup      INTEGER NOT NULL DEFAULT 1,
    updated_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS scans (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT    NOT NULL,
    profile         TEXT    NOT NULL,
    targets_json    TEXT    NOT NULL DEFAULT '[]',
    addresses_json  TEXT    NOT NULL DEFAULT '[]',
    dropped_json    TEXT    NOT NULL DEFAULT '[]',
    tcp_ports_json  TEXT    NOT NULL DEFAULT '[]',
    udp_ports_json  TEXT    NOT NULL DEFAULT '[]',
    options_json    TEXT    NOT NULL DEFAULT '{}',
    started_by      TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'queued'
                            CHECK(status IN ('queued','running','completed','failed','cancelled')),
    phase           TEXT    NOT NULL DEFAULT '',
    hosts_total     INTEGER NOT NULL DEFAULT 0,
    hosts_done      INTEGER NOT NULL DEFAULT 0,
    hosts_up        INTEGER NOT NULL DEFAULT 0,
    open_ports      INTEGER NOT NULL DEFAULT 0,
    findings_new    INTEGER NOT NULL DEFAULT 0,
    probes_sent     INTEGER NOT NULL DEFAULT 0,
    error           TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    started_at      TEXT,
    completed_at    TEXT
);
CREATE INDEX IF NOT EXISTS scans_project ON scans(project_id, id DESC);

CREATE TABLE IF NOT EXISTS hosts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    address     TEXT    NOT NULL,
    hostname    TEXT    NOT NULL DEFAULT '',
    up          INTEGER NOT NULL DEFAULT 0,
    first_seen  TEXT    NOT NULL,
    last_seen   TEXT    NOT NULL,
    last_scan_id INTEGER,
    UNIQUE(project_id, address)
);

CREATE TABLE IF NOT EXISTS services (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL,
    host_id     INTEGER NOT NULL REFERENCES hosts(id),
    port        INTEGER NOT NULL,
    protocol    TEXT    NOT NULL CHECK(protocol IN ('tcp','udp')),
    state       TEXT    NOT NULL DEFAULT 'open',
    service     TEXT    NOT NULL DEFAULT '',
    product     TEXT    NOT NULL DEFAULT '',
    version     TEXT    NOT NULL DEFAULT '',
    tls         INTEGER NOT NULL DEFAULT 0,
    banner      TEXT    NOT NULL DEFAULT '',
    details_json TEXT   NOT NULL DEFAULT '{}',
    first_seen  TEXT    NOT NULL,
    last_seen   TEXT    NOT NULL,
    last_scan_id INTEGER,
    UNIQUE(host_id, port, protocol)
);

-- One row per scan per open service: lets diffs compare two scans exactly.
CREATE TABLE IF NOT EXISTS scan_services (
    scan_id     INTEGER NOT NULL REFERENCES scans(id),
    address     TEXT    NOT NULL,
    port        INTEGER NOT NULL,
    protocol    TEXT    NOT NULL,
    service     TEXT    NOT NULL DEFAULT '',
    product     TEXT    NOT NULL DEFAULT '',
    version     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY(scan_id, address, port, protocol)
);

CREATE TABLE IF NOT EXISTS findings (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   TEXT    NOT NULL,
    address      TEXT    NOT NULL,
    port         INTEGER NOT NULL DEFAULT 0,
    protocol     TEXT    NOT NULL DEFAULT 'tcp',
    rule_id      TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    severity     TEXT    NOT NULL CHECK(severity IN ('critical','high','medium','low','info')),
    cvss         REAL    NOT NULL DEFAULT 0,
    epss         REAL    NOT NULL DEFAULT 0,
    kev          INTEGER NOT NULL DEFAULT 0,
    cve_ids_json TEXT    NOT NULL DEFAULT '[]',
    evidence     TEXT    NOT NULL DEFAULT '',
    remediation  TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'open'
                         CHECK(status IN ('open','confirmed','false_positive','risk_accepted','fixed')),
    note         TEXT    NOT NULL DEFAULT '',
    first_seen   TEXT    NOT NULL,
    last_seen    TEXT    NOT NULL,
    first_scan_id INTEGER,
    last_scan_id INTEGER,
    fixed_scan_id INTEGER,
    UNIQUE(project_id, address, port, protocol, rule_id)
);
CREATE INDEX IF NOT EXISTS findings_project_sev ON findings(project_id, status, severity);

-- Cached NVD lookups keyed by CPE match string.
CREATE TABLE IF NOT EXISTS cve_cache (
    cpe         TEXT    PRIMARY KEY,
    cves_json   TEXT    NOT NULL DEFAULT '[]',
    fetched_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS kev_cache (
    id          INTEGER PRIMARY KEY CHECK(id = 1),
    cves_json   TEXT    NOT NULL DEFAULT '[]',
    fetched_at  TEXT    NOT NULL
);
