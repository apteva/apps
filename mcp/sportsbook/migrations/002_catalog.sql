CREATE TABLE sports (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  PRIMARY KEY(project_id,id)
);
CREATE TABLE competitions (
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  sport TEXT NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  PRIMARY KEY(project_id,id),
  UNIQUE(project_id,sport,name COLLATE NOCASE),
  FOREIGN KEY(project_id,sport) REFERENCES sports(project_id,id)
);
CREATE TABLE sport_market_types (
  project_id TEXT NOT NULL,
  sport TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type='match_winner'),
  outcome_profile TEXT NOT NULL CHECK(outcome_profile IN ('two_way','three_way')),
  rules TEXT NOT NULL CHECK(rules IN ('regulation','match_completed')),
  prediction_model TEXT NOT NULL CHECK(prediction_model IN ('elo','baseline','none')),
  history_scope TEXT NOT NULL CHECK(history_scope IN ('sport','competition')),
  home_advantage INTEGER NOT NULL CHECK(home_advantage BETWEEN 0 AND 200),
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  PRIMARY KEY(project_id,sport,type),
  FOREIGN KEY(project_id,sport) REFERENCES sports(project_id,id)
);
CREATE TABLE provider_sport_mappings (
  project_id TEXT NOT NULL,
  sport TEXT NOT NULL,
  competition_id TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL CHECK(role IN ('sports_data','odds')),
  provider_slug TEXT NOT NULL,
  external_key TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  PRIMARY KEY(project_id,sport,competition_id,role,provider_slug),
  FOREIGN KEY(project_id,sport) REFERENCES sports(project_id,id)
);
ALTER TABLE events ADD COLUMN competition_id TEXT NOT NULL DEFAULT '';
ALTER TABLE markets ADD COLUMN outcome_profile TEXT NOT NULL DEFAULT 'two_way';
ALTER TABLE markets ADD COLUMN prediction_model TEXT NOT NULL DEFAULT 'elo';
ALTER TABLE markets ADD COLUMN history_scope TEXT NOT NULL DEFAULT 'sport';
ALTER TABLE markets ADD COLUMN home_advantage INTEGER NOT NULL DEFAULT 0;
UPDATE markets SET outcome_profile='three_way',history_scope='competition',home_advantage=60
 WHERE EXISTS(SELECT 1 FROM events e WHERE e.project_id=markets.project_id AND e.id=markets.event_id AND e.sport='football');
CREATE INDEX competition_schedule ON events(project_id,competition_id,starts_at);
ALTER TABLE quote_heads ADD COLUMN feed_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN score_rules TEXT NOT NULL DEFAULT '';
UPDATE events SET score_rules=CASE sport WHEN 'football' THEN 'regulation' ELSE 'match_completed' END;
