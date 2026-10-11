-- Additive diagnostics: retain legacy timings and expose submillisecond phases.
ALTER TABLE read_diagnostics ADD COLUMN statement_prepare_ms REAL NOT NULL DEFAULT 0;
ALTER TABLE read_diagnostics ADD COLUMN statement_bind_ms REAL NOT NULL DEFAULT 0;
ALTER TABLE read_diagnostics ADD COLUMN statement_execute_ms REAL NOT NULL DEFAULT 0;
ALTER TABLE read_diagnostics ADD COLUMN statement_scan_ms REAL NOT NULL DEFAULT 0;
ALTER TABLE read_diagnostics ADD COLUMN statement_cache TEXT NOT NULL DEFAULT '';
