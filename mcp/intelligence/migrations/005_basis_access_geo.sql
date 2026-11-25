-- Basis, access and geography.
--
-- basis says what kind of knowledge a row is:
--   observed  — a source said it (connector or operator-recorded evidence)
--   inferred  — the app or a model derived it (LLM extraction, inferred or
--               assumed relationships)
--   synthetic — generated sample data; never used by forecasts or the ledger
--
-- access classifies who may read evidence: open (public sources),
-- internal (licensed or operator data), restricted (agents need an
-- explicit grant). Evidence takes its source's class unless a stricter one
-- is given.

ALTER TABLE intelligence_evidence ADD COLUMN basis TEXT NOT NULL DEFAULT 'observed';
ALTER TABLE intelligence_evidence ADD COLUMN access TEXT NOT NULL DEFAULT 'open';
-- Set when the source's retention period removed the content. Metadata and
-- the content hash stay, so forecast replays still verify.
ALTER TABLE intelligence_evidence ADD COLUMN content_expired_at TIMESTAMP;
CREATE INDEX ix_intel_evidence_basis ON intelligence_evidence(project_id, basis);

ALTER TABLE intel_sources ADD COLUMN access TEXT NOT NULL DEFAULT 'open';

ALTER TABLE entities ADD COLUMN basis TEXT NOT NULL DEFAULT 'observed';
ALTER TABLE intel_claims ADD COLUMN basis TEXT NOT NULL DEFAULT 'inferred';
ALTER TABLE intel_state_values ADD COLUMN basis TEXT NOT NULL DEFAULT 'observed';
ALTER TABLE intel_relationships ADD COLUMN basis TEXT NOT NULL DEFAULT 'observed';
UPDATE intel_relationships SET basis = 'inferred' WHERE status IN ('inferred', 'assumed');
-- Entities the LLM extractor created are inferences about the text.
UPDATE entities SET basis = 'inferred'
 WHERE kind <> 'indicator' AND id IN (SELECT subject_entity_id FROM intel_claims WHERE extractor LIKE 'llm:%' AND subject_entity_id IS NOT NULL)
   AND id NOT IN (SELECT entity_id FROM intel_state_values);

-- Where an entity is. Each row is attributed: which source placed it, at
-- what precision, and from which evidence. Several sources may disagree;
-- the data-quality check reports that as a conflict instead of picking one.
CREATE TABLE intel_entity_locations (
  id           INTEGER PRIMARY KEY,
  project_id   TEXT NOT NULL,
  entity_id    INTEGER NOT NULL,
  lat          REAL NOT NULL,
  lon          REAL NOT NULL,
  label        TEXT NOT NULL DEFAULT '',
  country_id   TEXT NOT NULL DEFAULT '',   -- Natural Earth / ISO 3166 numeric
  precision    TEXT NOT NULL,              -- point | city | region | country
  provenance   TEXT NOT NULL,              -- how the coordinates were obtained
  source       TEXT NOT NULL,              -- gazetteer | wikipedia | operator | synthetic | ...
  evidence_id  INTEGER,
  basis        TEXT NOT NULL DEFAULT 'observed',
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  retracted_at TIMESTAMP
);
CREATE UNIQUE INDEX ux_intel_entity_loc ON intel_entity_locations(project_id, entity_id, source);
CREATE INDEX ix_intel_entity_loc ON intel_entity_locations(project_id, entity_id);

-- Entities the geocoder already tried, so a miss is reported once and not
-- re-requested every cycle.
CREATE TABLE intel_geocode_attempts (
  project_id   TEXT NOT NULL,
  entity_id    INTEGER NOT NULL,
  source       TEXT NOT NULL,
  attempted_at TIMESTAMP NOT NULL,
  outcome      TEXT NOT NULL,              -- found | not_found | failed
  detail       TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, entity_id, source)
);
