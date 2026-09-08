CREATE TABLE gig_agreements (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, gig_id INTEGER NOT NULL REFERENCES gigs(id),
 revision INTEGER NOT NULL, snapshot_json TEXT NOT NULL, actor TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(gig_id,revision)
);
CREATE TABLE gig_obligations (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, gig_id INTEGER NOT NULL REFERENCES gigs(id),
 agreement_id TEXT NOT NULL REFERENCES gig_agreements(id), request_key TEXT NOT NULL,
 snapshot_json TEXT NOT NULL, actor TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 bills_install_id INTEGER, bill_id INTEGER, vendor_id INTEGER, source_key TEXT,
 request_json TEXT, documents_synced INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'not_connected', sync_error TEXT,
 bill_json TEXT, synced_at TEXT, next_sync_at TEXT,
 UNIQUE(project_id,request_key)
);
CREATE INDEX gig_obligations_gig ON gig_obligations(project_id,gig_id,created_at);
CREATE INDEX gig_obligations_sync ON gig_obligations(next_sync_at);
CREATE TRIGGER gig_agreements_immutable BEFORE UPDATE ON gig_agreements BEGIN SELECT RAISE(ABORT,'agreements are immutable; append a revision'); END;
CREATE TRIGGER gig_obligations_immutable BEFORE UPDATE OF snapshot_json,agreement_id,actor,created_at,gig_id,project_id,request_key ON gig_obligations BEGIN SELECT RAISE(ABORT,'approved obligations are immutable'); END;
