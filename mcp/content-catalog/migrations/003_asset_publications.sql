-- Asset-first publication records. Legacy release tables remain untouched for
-- rollback and historical audit; each old target/asset pair becomes one record.
CREATE TABLE asset_publications (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  asset_id TEXT NOT NULL REFERENCES assets(id),
  destination TEXT NOT NULL,
  account_ref TEXT NOT NULL DEFAULT '',
  audience TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','scheduled','submitted','provider_reported_published','verified_published','failed','removed','unknown')),
  planned_at TEXT NOT NULL DEFAULT '',
  actual_at TEXT NOT NULL DEFAULT '',
  external_post_id TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  evidence_source TEXT NOT NULL DEFAULT '',
  failure_details TEXT NOT NULL DEFAULT '',
  legacy_target_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_asset_publications_asset ON asset_publications(project_id, asset_id, created_at DESC);
CREATE INDEX ix_asset_publications_destination ON asset_publications(project_id, destination, account_ref, status);
CREATE UNIQUE INDEX ix_asset_publications_legacy ON asset_publications(project_id, legacy_target_id, asset_id) WHERE legacy_target_id != '';

CREATE TABLE asset_publication_events (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  publication_id TEXT NOT NULL REFERENCES asset_publications(id),
  status TEXT NOT NULL,
  external_post_id TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  actual_at TEXT NOT NULL DEFAULT '',
  evidence_source TEXT NOT NULL DEFAULT '',
  failure_details TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_asset_publication_events ON asset_publication_events(project_id, publication_id, observed_at DESC);

INSERT INTO asset_publications
  (id,project_id,asset_id,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,legacy_target_id,created_at,updated_at)
SELECT t.id || ':' || ra.asset_id, ra.project_id, ra.asset_id, t.destination, t.account_ref, r.audience,
       t.current_status, COALESCE(NULLIF(t.planned_at,''),r.planned_at,''),
       COALESCE(o.actual_at,''), COALESCE(o.external_post_id,''), COALESCE(o.external_url,''),
       COALESCE(o.evidence_source,''), COALESCE(o.failure_details,''), t.id, t.created_at,
       COALESCE(o.observed_at,t.created_at)
FROM release_target_assets ra
JOIN release_targets t ON t.id=ra.target_id AND t.project_id=ra.project_id
JOIN releases r ON r.id=t.release_id AND r.project_id=t.project_id
LEFT JOIN publication_observations o ON o.id=(
  SELECT po.id FROM publication_observations po WHERE po.project_id=t.project_id AND po.target_id=t.id
  ORDER BY po.observed_at DESC,po.id DESC LIMIT 1
);

INSERT INTO asset_publication_events
  (id,project_id,publication_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details,observed_at)
SELECT po.id || ':' || ra.asset_id, po.project_id, t.id || ':' || ra.asset_id,
       po.status, po.external_post_id, po.external_url, po.actual_at,
       po.evidence_source, po.failure_details, po.observed_at
FROM publication_observations po
JOIN release_targets t ON t.id=po.target_id AND t.project_id=po.project_id
JOIN release_target_assets ra ON ra.target_id=t.id AND ra.project_id=t.project_id;
