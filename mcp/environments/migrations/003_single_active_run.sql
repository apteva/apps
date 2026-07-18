UPDATE environment_runs AS current
SET status = 'expired',
    error = 'superseded by a newer active run',
    stopped_at = COALESCE(stopped_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
WHERE environment_id <> ''
  AND status IN ('starting', 'running', 'stopping')
  AND EXISTS (
    SELECT 1
    FROM environment_runs AS newer
    WHERE newer.environment_id = current.environment_id
      AND newer.status IN ('starting', 'running', 'stopping')
      AND (
        newer.started_at > current.started_at
        OR (newer.started_at = current.started_at AND newer.id > current.id)
      )
  );

UPDATE environment_web_fixtures
SET status = 'expired',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE run_id IN (
  SELECT id
  FROM environment_runs
  WHERE status = 'expired'
    AND error = 'superseded by a newer active run'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_environment_runs_one_active
ON environment_runs(environment_id)
WHERE environment_id <> ''
  AND status IN ('starting', 'running', 'stopping');
