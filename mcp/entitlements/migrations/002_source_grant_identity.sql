-- Entitlements v0.2.0 -- one active projection per source-scoped grant.

UPDATE entitlement_grants
SET status = 'revoked',
    revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP),
    revoked_reason = COALESCE(revoked_reason, 'superseded during source grant deduplication'),
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'active'
  AND source_id IS NOT NULL
  AND source_id != ''
  AND id NOT IN (
    SELECT MAX(id)
    FROM entitlement_grants
    WHERE status = 'active'
      AND source_id IS NOT NULL
      AND source_id != ''
    GROUP BY project_id, subject_type, subject_id, feature_key, source_type, source_id
  );

CREATE UNIQUE INDEX ux_grants_active_source
  ON entitlement_grants(project_id, subject_type, subject_id, feature_key, source_type, source_id)
  WHERE status = 'active' AND source_id IS NOT NULL AND source_id != '';
