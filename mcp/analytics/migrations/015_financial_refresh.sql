-- Durable project invalidations live in the same transaction as every input mutation.
CREATE TABLE financial_projects (
 project_id TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1, fx_enabled INTEGER NOT NULL DEFAULT 1,
 provider TEXT NOT NULL DEFAULT 'ecb', fx_cursor INTEGER NOT NULL DEFAULT 0, revision INTEGER NOT NULL DEFAULT 1,
 lease_token TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 last_attempt INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE financial_targets (
 target_id INTEGER PRIMARY KEY REFERENCES objective_targets(id) ON DELETE CASCADE,
 input_revision INTEGER NOT NULL DEFAULT 0, definition_revision INTEGER NOT NULL DEFAULT 0,
 last_attempt INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 retry_count INTEGER NOT NULL DEFAULT 0, next_retry INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'pending',
 verified_by TEXT NOT NULL DEFAULT '', verified_at INTEGER NOT NULL DEFAULT 0,
 verified_revision INTEGER NOT NULL DEFAULT 0, verified_through INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE financial_fx_requests (
 project_id TEXT NOT NULL, base TEXT NOT NULL, quote TEXT NOT NULL, day TEXT NOT NULL, required_at INTEGER NOT NULL DEFAULT 0,
 last_attempt INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 retry_count INTEGER NOT NULL DEFAULT 0, next_retry INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
 provenance TEXT NOT NULL DEFAULT '{}', PRIMARY KEY(project_id,base,quote,day)
);
-- Consent is granted inside the source project, accepted separately inside the
-- destination project, and checked on every use. Changing the source definition
-- invalidates consent; an editor must grant it again.
CREATE TABLE financial_shares (
 id TEXT PRIMARY KEY, source_project TEXT NOT NULL, target_id INTEGER NOT NULL REFERENCES objective_targets(id),
 destination_project TEXT NOT NULL, definition_revision INTEGER NOT NULL,
 metric_meaning TEXT NOT NULL CHECK(metric_meaning IN ('revenue','realized_profit','other')),
 economic_key TEXT NOT NULL, approved_by TEXT NOT NULL, approved_at INTEGER NOT NULL, revoked_at INTEGER,
 CHECK(source_project!=destination_project)
);
CREATE TABLE financial_mappings (
 id TEXT PRIMARY KEY, destination_project TEXT NOT NULL, destination_target INTEGER NOT NULL REFERENCES objective_targets(id),
 share_id TEXT NOT NULL REFERENCES financial_shares(id), component_event_id INTEGER NOT NULL UNIQUE REFERENCES events(id),
 definition_revision INTEGER NOT NULL, approved_by TEXT NOT NULL, approved_at INTEGER NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1, last_attempt INTEGER NOT NULL DEFAULT 0,
 last_success INTEGER NOT NULL DEFAULT 0, source_measured_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', UNIQUE(destination_target,share_id)
);
CREATE INDEX ix_financial_mapping_destination ON financial_mappings(destination_project,enabled);
INSERT INTO financial_projects(project_id) SELECT DISTINCT project_id FROM objectives;
CREATE TRIGGER financial_events_insert AFTER INSERT ON events BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_events_update AFTER UPDATE ON events BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_events_delete AFTER DELETE ON events BEGIN
 INSERT INTO financial_projects(project_id) SELECT OLD.project_id WHERE OLD.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_fx_rates_insert AFTER INSERT ON fx_rates BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_fx_rates_update AFTER UPDATE ON fx_rates WHEN OLD.rate!=NEW.rate OR OLD.source!=NEW.source OR OLD.as_of!=NEW.as_of BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_fx_rates_delete AFTER DELETE ON fx_rates BEGIN
 INSERT INTO financial_projects(project_id) SELECT OLD.project_id WHERE OLD.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objectives_insert AFTER INSERT ON objectives BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objectives_update AFTER UPDATE ON objectives BEGIN
 INSERT INTO financial_projects(project_id) SELECT NEW.project_id WHERE NEW.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objectives_delete AFTER DELETE ON objectives BEGIN
 INSERT INTO financial_projects(project_id) SELECT OLD.project_id WHERE OLD.project_id IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objective_targets_insert AFTER INSERT ON objective_targets BEGIN
 INSERT INTO financial_projects(project_id) SELECT (SELECT project_id FROM objectives WHERE id=NEW.objective_id) WHERE (SELECT project_id FROM objectives WHERE id=NEW.objective_id) IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objective_targets_update AFTER UPDATE ON objective_targets BEGIN
 INSERT INTO financial_projects(project_id) SELECT (SELECT project_id FROM objectives WHERE id=NEW.objective_id) WHERE (SELECT project_id FROM objectives WHERE id=NEW.objective_id) IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_objective_targets_delete AFTER DELETE ON objective_targets BEGIN
 INSERT INTO financial_projects(project_id) SELECT (SELECT project_id FROM objectives WHERE id=OLD.objective_id) WHERE (SELECT project_id FROM objectives WHERE id=OLD.objective_id) IS NOT NULL
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
-- Revocation/errors invalidate destination caches atomically, even if a worker
-- is evaluating them concurrently. Sharing never enables ordinary broad reads.
CREATE TRIGGER financial_share_revoke AFTER UPDATE ON financial_shares BEGIN
 INSERT INTO financial_projects(project_id) VALUES(NEW.destination_project)
 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER financial_mapping_change AFTER UPDATE ON financial_mappings
 WHEN OLD.last_error!=NEW.last_error OR OLD.enabled!=NEW.enabled BEGIN
 UPDATE financial_projects SET revision=revision+1 WHERE project_id=NEW.destination_project;
END;
CREATE TABLE financial_fx_provenance (
 revision_id INTEGER PRIMARY KEY REFERENCES fx_rate_revisions(id),
 project_id TEXT NOT NULL, imported_at INTEGER NOT NULL,
 observations TEXT NOT NULL CHECK(json_valid(observations))
);
CREATE TRIGGER financial_fx_provenance_immutable_update BEFORE UPDATE ON financial_fx_provenance BEGIN SELECT RAISE(ABORT,'FX provenance is immutable'); END;
CREATE TRIGGER financial_fx_provenance_immutable_delete BEFORE DELETE ON financial_fx_provenance BEGIN SELECT RAISE(ABORT,'FX provenance is immutable'); END;
