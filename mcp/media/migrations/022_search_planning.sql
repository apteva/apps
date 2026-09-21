-- Stable, planning-oriented media_search ordering and bounded inventory facets.
-- These expression indexes keep common editorial JSON metadata out of table
-- scans while preserving metadata as the consumer-owned source of truth.

CREATE INDEX IF NOT EXISTS ix_media_search_created
  ON media(project_id, strftime('%Y-%m-%dT%H:%M:%SZ', created_at) DESC, file_id DESC)
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_updated
  ON media(project_id, strftime('%Y-%m-%dT%H:%M:%SZ', updated_at) DESC, file_id DESC)
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_recording_date
  ON media(project_id, CAST(json_extract(metadata, '$.recording_date') AS TEXT) DESC, file_id DESC)
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_session_date
  ON media(project_id,
    COALESCE(CAST(json_extract(metadata, '$.session.date') AS TEXT), CAST(json_extract(metadata, '$.session_date') AS TEXT), '') DESC,
    file_id DESC)
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_patreon
  ON media(project_id,
    CASE lower(COALESCE(CAST(json_extract(metadata, '$.patreon.status') AS TEXT), ''))
      WHEN 'ready' THEN 5 WHEN 'scheduled' THEN 4
      WHEN 'draft' THEN 3 WHEN 'pending' THEN 3 WHEN 'review' THEN 3
      WHEN 'published' THEN 2 WHEN 'shared' THEN 2 WHEN 'posted' THEN 2
      WHEN 'blocked' THEN 1 WHEN 'failed' THEN 1 ELSE 0 END DESC,
    file_id DESC)
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_model
  ON media(project_id, CAST(json_extract(metadata, '$.model') AS TEXT))
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_session
  ON media(project_id,
    COALESCE(CAST(json_extract(metadata, '$.lineage.session_id') AS TEXT), CAST(json_extract(metadata, '$.session.id') AS TEXT), CAST(json_extract(metadata, '$.session_id') AS TEXT), ''))
  WHERE probe_status='ok';

CREATE INDEX IF NOT EXISTS ix_media_search_hosting
  ON media(project_id,
    CASE
      WHEN json_type(metadata, '$.hosting.ready') IN ('true','integer')
       AND json_extract(metadata, '$.hosting.ready') IN (1, 'true') THEN 1
      WHEN lower(COALESCE(CAST(json_extract(metadata, '$.hosting.status') AS TEXT), ''))
       IN ('ready','hosted','published','complete','completed','available') THEN 1
      ELSE 0 END DESC,
    file_id DESC)
  WHERE probe_status='ok';
