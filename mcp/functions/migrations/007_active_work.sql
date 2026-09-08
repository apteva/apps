-- Only active work is indexed. Do not build an index over historical payloads
-- during startup: creating that index would itself scan the whole database.
CREATE TABLE function_runtime_owners (id TEXT PRIMARY KEY);
CREATE TABLE function_active_work (
  kind TEXT NOT NULL CHECK(kind IN ('invocation','build')),
  id INTEGER NOT NULL,
  owner TEXT NOT NULL REFERENCES function_runtime_owners(id),
  PRIMARY KEY(kind,id)
);
CREATE INDEX ix_active_work_owner ON function_active_work(owner);
CREATE TRIGGER invocation_work_finished AFTER UPDATE OF status ON function_invocations
WHEN NEW.status != 'running' BEGIN
  DELETE FROM function_active_work WHERE kind='invocation' AND id=NEW.id;
END;
CREATE TRIGGER invocation_work_deleted AFTER DELETE ON function_invocations BEGIN
  DELETE FROM function_active_work WHERE kind='invocation' AND id=OLD.id;
END;
CREATE TRIGGER build_work_finished AFTER UPDATE OF build_status ON function_versions
WHEN NEW.build_status NOT IN ('pending','building') BEGIN
  DELETE FROM function_active_work WHERE kind='build' AND id=NEW.id;
END;
CREATE TRIGGER build_work_deleted AFTER DELETE ON function_versions BEGIN
  DELETE FROM function_active_work WHERE kind='build' AND id=OLD.id;
END;
CREATE TABLE function_recovery_progress (kind TEXT PRIMARY KEY, cursor INTEGER NOT NULL, ceiling INTEGER NOT NULL);
