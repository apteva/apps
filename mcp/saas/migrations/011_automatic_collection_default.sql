-- SaaS v0.8.1 — make automatic collection the default without changing
-- the behavior of paid plans created before this release.

UPDATE saas_plans
SET metadata_json = json_set(
      CASE WHEN json_valid(metadata_json) THEN metadata_json ELSE '{}' END,
      '$.collection_method',
      'send_invoice'
    ),
    updated_at = CURRENT_TIMESTAMP
WHERE billing_mode = 'paid'
  AND COALESCE(
        NULLIF(
          TRIM(
            json_extract(
              CASE WHEN json_valid(metadata_json) THEN metadata_json ELSE '{}' END,
              '$.collection_method'
            )
          ),
          ''
        ),
        ''
      ) = '';
