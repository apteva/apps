CREATE TABLE fx_rate_revisions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, rate_id INTEGER NOT NULL,
 project_id TEXT NOT NULL, base_currency TEXT NOT NULL, quote_currency TEXT NOT NULL,
 as_of INTEGER NOT NULL, rate REAL NOT NULL, source TEXT NOT NULL, recorded_at INTEGER NOT NULL
);
INSERT INTO fx_rate_revisions(rate_id,project_id,base_currency,quote_currency,as_of,rate,source,recorded_at)
 SELECT id,project_id,base_currency,quote_currency,as_of,rate,source,updated_at FROM fx_rates;
CREATE INDEX ix_fx_revisions ON fx_rate_revisions(project_id,recorded_at,id);
CREATE TRIGGER fx_revision_insert AFTER INSERT ON fx_rates BEGIN
 INSERT INTO fx_rate_revisions(rate_id,project_id,base_currency,quote_currency,as_of,rate,source,recorded_at)
 VALUES(NEW.id,NEW.project_id,NEW.base_currency,NEW.quote_currency,NEW.as_of,NEW.rate,NEW.source,NEW.updated_at);
END;
CREATE TRIGGER fx_revision_update AFTER UPDATE ON fx_rates WHEN OLD.rate!=NEW.rate OR OLD.source!=NEW.source BEGIN
 INSERT INTO fx_rate_revisions(rate_id,project_id,base_currency,quote_currency,as_of,rate,source,recorded_at)
 VALUES(NEW.id,NEW.project_id,NEW.base_currency,NEW.quote_currency,NEW.as_of,NEW.rate,NEW.source,NEW.updated_at);
END;
CREATE TRIGGER fx_revision_immutable_update BEFORE UPDATE ON fx_rate_revisions BEGIN SELECT RAISE(ABORT,'FX revisions are immutable'); END;
CREATE TRIGGER fx_revision_immutable_delete BEFORE DELETE ON fx_rate_revisions BEGIN SELECT RAISE(ABORT,'FX revisions are immutable'); END;
