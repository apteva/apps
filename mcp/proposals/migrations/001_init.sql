CREATE TABLE proposals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL
);
CREATE INDEX proposals_project ON proposals(project_id, id DESC);

CREATE TABLE revisions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id),
    number INTEGER NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL CHECK (state IN ('draft', 'published', 'superseded', 'withdrawn', 'accepted', 'declined', 'changes_requested')),
    content TEXT NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0,
    link_nonce TEXT,
    published_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (proposal_id, number)
);
CREATE UNIQUE INDEX revisions_draft ON revisions(proposal_id) WHERE state = 'draft';
CREATE UNIQUE INDEX revisions_published ON revisions(proposal_id) WHERE state = 'published';
CREATE UNIQUE INDEX revisions_link_nonce ON revisions(link_nonce) WHERE link_nonce IS NOT NULL;


CREATE TABLE responses (
    revision_id INTEGER PRIMARY KEY REFERENCES revisions(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'declined', 'changes_requested')),
    name TEXT NOT NULL,
    note TEXT NOT NULL,
    selection TEXT NOT NULL,
    total_minor INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    topic TEXT NOT NULL,
    payload TEXT NOT NULL,
    delivered_at INTEGER
);
CREATE INDEX outbox_pending ON outbox(project_id, id) WHERE delivered_at IS NULL;

CREATE TABLE app_secrets (
    name TEXT PRIMARY KEY,
    secret BLOB NOT NULL
);
