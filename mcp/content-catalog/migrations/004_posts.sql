-- A post is one destination outcome shared by one or more assets. The v0.3
-- asset-publication tables remain as a read-only migration archive.
CREATE TABLE posts (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  brand_id TEXT NOT NULL REFERENCES brands(id),
  title TEXT NOT NULL DEFAULT '',
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
CREATE INDEX ix_posts_brand ON posts(project_id,brand_id,created_at DESC);
CREATE INDEX ix_posts_destination ON posts(project_id,destination,account_ref,status);

CREATE TABLE post_assets (
  project_id TEXT NOT NULL,
  post_id TEXT NOT NULL REFERENCES posts(id),
  asset_id TEXT NOT NULL REFERENCES assets(id),
  position INTEGER NOT NULL,
  PRIMARY KEY(project_id,post_id,asset_id),
  UNIQUE(project_id,post_id,position)
);
CREATE INDEX ix_post_assets_asset ON post_assets(project_id,asset_id,post_id);

CREATE TABLE post_events (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  post_id TEXT NOT NULL REFERENCES posts(id),
  status TEXT NOT NULL,
  external_post_id TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  actual_at TEXT NOT NULL DEFAULT '',
  evidence_source TEXT NOT NULL DEFAULT '',
  failure_details TEXT NOT NULL DEFAULT '',
  asset_ids_json TEXT NOT NULL DEFAULT '[]',
  observed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_post_events_post ON post_events(project_id,post_id,observed_at DESC);

-- A former release target is one post only when every copied asset row still
-- agrees on the post-level fields. Divergent edits stay independent posts.
CREATE TABLE post_migration_map (
  project_id TEXT NOT NULL,
  old_publication_id TEXT NOT NULL,
  post_id TEXT NOT NULL,
  PRIMARY KEY(project_id,old_publication_id)
);
INSERT INTO post_migration_map(project_id,old_publication_id,post_id)
SELECT p.project_id,p.id,
  CASE WHEN p.legacy_target_id!='' AND EXISTS (
    SELECT 1 FROM asset_publications q
    WHERE q.project_id=p.project_id AND q.legacy_target_id=p.legacy_target_id
    GROUP BY q.project_id,q.legacy_target_id
    HAVING MIN(q.destination)=MAX(q.destination)
       AND MIN(q.account_ref)=MAX(q.account_ref)
       AND MIN(q.audience)=MAX(q.audience)
       AND MIN(q.status)=MAX(q.status)
       AND MIN(q.planned_at)=MAX(q.planned_at)
       AND MIN(q.actual_at)=MAX(q.actual_at)
       AND MIN(q.external_post_id)=MAX(q.external_post_id)
       AND MIN(q.external_url)=MAX(q.external_url)
       AND MIN(q.evidence_source)=MAX(q.evidence_source)
       AND MIN(q.failure_details)=MAX(q.failure_details)
  ) THEN p.legacy_target_id ELSE p.id END
FROM asset_publications p;

INSERT INTO posts(id,project_id,brand_id,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,legacy_target_id,created_at,updated_at)
SELECT m.post_id,p.project_id,MIN(s.brand_id),MIN(p.destination),MIN(p.account_ref),MIN(p.audience),MIN(p.status),MIN(p.planned_at),MIN(p.actual_at),MIN(p.external_post_id),MIN(p.external_url),MIN(p.evidence_source),MIN(p.failure_details),MIN(p.legacy_target_id),MIN(p.created_at),MAX(p.updated_at)
FROM post_migration_map m
JOIN asset_publications p ON p.project_id=m.project_id AND p.id=m.old_publication_id
JOIN assets a ON a.project_id=p.project_id AND a.id=p.asset_id
JOIN sessions s ON s.project_id=a.project_id AND s.id=a.session_id
GROUP BY p.project_id,m.post_id;

INSERT INTO post_assets(project_id,post_id,asset_id,position)
SELECT p.project_id,m.post_id,p.asset_id,
  ROW_NUMBER() OVER (PARTITION BY p.project_id,m.post_id ORDER BY COALESCE(rta.position,999999),p.created_at,p.asset_id)-1
FROM post_migration_map m
JOIN asset_publications p ON p.project_id=m.project_id AND p.id=m.old_publication_id
LEFT JOIN release_target_assets rta ON rta.project_id=p.project_id AND rta.target_id=p.legacy_target_id AND rta.asset_id=p.asset_id;

INSERT OR IGNORE INTO post_events(id,project_id,post_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details,observed_at)
SELECT CASE WHEN p.legacy_target_id!='' AND m.post_id=p.legacy_target_id
                 AND substr(e.id,-length(p.asset_id)-1)=':'||p.asset_id
            THEN substr(e.id,1,length(e.id)-length(p.asset_id)-1)
            ELSE e.id END,
       e.project_id,m.post_id,e.status,e.external_post_id,e.external_url,e.actual_at,e.evidence_source,e.failure_details,e.observed_at
FROM asset_publication_events e
JOIN asset_publications p ON p.project_id=e.project_id AND p.id=e.publication_id
JOIN post_migration_map m ON m.project_id=p.project_id AND m.old_publication_id=p.id;
