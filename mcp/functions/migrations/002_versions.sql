-- Functions v1 — deploy-time build + immutable versioning.
--
-- A function is now a thin definition (name, runtime, env, limits)
-- that points at an active version. Each functions_deploy creates an
-- immutable function_versions row and builds it once — `bun install`
-- when a package_json is supplied, otherwise just stages the entry
-- file. On a successful build the new version becomes active;
-- functions_rollback just repoints active_version_id at an older row.
--
-- The functions table keeps source / source_hash columns: they hold
-- the *active* version's values, denormalised so the hot invoke path
-- and the warm-worker pool don't need a join. function_versions is
-- the source of truth and the history.

ALTER TABLE functions ADD COLUMN active_version_id INTEGER;

CREATE TABLE function_versions (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT    NOT NULL,
  function_id   INTEGER NOT NULL REFERENCES functions(id) ON DELETE CASCADE,
  version       INTEGER NOT NULL,                    -- monotonic per function, 1-based

  source_kind   TEXT    NOT NULL DEFAULT 'inline',   -- inline | repo
  source        TEXT,                                 -- inline body
  repo_id       INTEGER,
  repo_path     TEXT,
  source_hash   TEXT    NOT NULL,                    -- sha256 of resolved source bytes
  package_json  TEXT,                                 -- optional deps manifest

  build_status  TEXT    NOT NULL DEFAULT 'pending',  -- pending | building | ready | failed
  build_log     TEXT,
  build_dir     TEXT,                                 -- deterministic path to the built artifact dir

  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

  UNIQUE(function_id, version)
);
CREATE INDEX ix_fnver_fn ON function_versions(function_id, version DESC);
