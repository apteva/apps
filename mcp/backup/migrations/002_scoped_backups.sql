-- Scoped backups let Backup store platform-wide runs and app-provider
-- runs in one history. Empty source_app/scope_id with scope_kind=platform
-- preserves the v0.1 behavior.

ALTER TABLE policies ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'platform';
ALTER TABLE policies ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN source_app TEXT NOT NULL DEFAULT '';

ALTER TABLE runs ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'platform';
ALTER TABLE runs ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN source_app TEXT NOT NULL DEFAULT '';

CREATE INDEX ix_policies_scope ON policies(scope_kind, scope_id, source_app);
CREATE INDEX ix_runs_scope ON runs(scope_kind, scope_id, source_app, started_at DESC);
