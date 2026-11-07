CREATE TABLE clients (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    crm_contact_id INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX clients_project ON clients(project_id, id DESC);
CREATE UNIQUE INDEX clients_crm ON clients(project_id, crm_contact_id) WHERE crm_contact_id > 0;
CREATE UNIQUE INDEX clients_email ON clients(project_id, lower(email)) WHERE email <> '';

CREATE TABLE backlog_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    client_id INTEGER NOT NULL REFERENCES clients(id),
    name TEXT NOT NULL,
    category TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    amount_minor INTEGER,
    currency TEXT NOT NULL,
    required INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX backlog_client ON backlog_items(client_id, archived, id DESC);

CREATE TABLE backlog_proposal_requests (
    project_id TEXT NOT NULL,
    request_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id),
    PRIMARY KEY (project_id, request_key)
);
