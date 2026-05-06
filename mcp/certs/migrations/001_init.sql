-- Apteva Certs v0.1 — TLS certificate issuance via ACME DNS-01.
--
-- Two tables:
--   acme_accounts — one row per (directory, email). The account key
--                   (RSA private) signs all order requests; LE binds
--                   it to a stable account URL we re-use forever.
--   certs         — issued + in-flight certs. We never delete on
--                   revoke; status='revoked' keeps the audit trail.

CREATE TABLE acme_accounts (
    id            INTEGER PRIMARY KEY,
    directory_url TEXT      NOT NULL,
    email         TEXT      NOT NULL,
    account_key   BLOB      NOT NULL,                    -- PEM-encoded private key (PKCS#8)
    account_url   TEXT      NOT NULL,                    -- LE-assigned account URL
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (directory_url, email)
);

CREATE TABLE certs (
    id              INTEGER PRIMARY KEY,
    project_id      TEXT      NOT NULL,
    fqdn            TEXT      NOT NULL,                  -- single-name in v0.1
    status          TEXT      NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','issuing','live','failed','revoked')),
    cert_pem        BLOB,                                -- full chain
    key_pem         BLOB,                                -- private key (PEM)
    serial          TEXT      NOT NULL DEFAULT '',
    issued_at       TIMESTAMP,
    expires_at      TIMESTAMP,
    last_renewed_at TIMESTAMP,
    last_attempt_at TIMESTAMP,
    error           TEXT      NOT NULL DEFAULT '',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, fqdn)
);
CREATE INDEX ix_certs_status_expires ON certs(status, expires_at);
