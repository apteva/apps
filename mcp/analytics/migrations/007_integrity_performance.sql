-- Analytics v0.8.5 - date-correct aggregate observations and the composite
-- index used by project-scoped dashboard widgets.

CREATE INDEX IF NOT EXISTS ix_events_project_app_topic_ts
ON events(project_id, app, topic, ts);

-- Existing daily specs conventionally expose the observation date as
-- props.date. Make that field authoritative for policy bucketing so delayed
-- imports do not create duplicates under their ingestion date.
UPDATE event_specs
SET upsert_policy = json_set(upsert_policy, '$.timestamp_property', 'props.date')
WHERE upsert_policy IS NOT NULL
  AND json_valid(upsert_policy)
  AND COALESCE(json_extract(upsert_policy, '$.bucket'), 'none') != 'none'
  AND json_extract(upsert_policy, '$.timestamp_property') IS NULL
  AND EXISTS (
    SELECT 1 FROM event_property_specs
    WHERE event_property_specs.event_spec_id = event_specs.id
      AND event_property_specs.key = 'props.date'
  );

UPDATE event_specs
SET rollup_policy = json_set(rollup_policy, '$.timestamp_property', 'props.date')
WHERE rollup_policy IS NOT NULL
  AND json_valid(rollup_policy)
  AND COALESCE(json_extract(rollup_policy, '$.bucket'), 'none') != 'none'
  AND json_extract(rollup_policy, '$.timestamp_property') IS NULL
  AND EXISTS (
    SELECT 1 FROM event_property_specs
    WHERE event_property_specs.event_spec_id = event_specs.id
      AND event_property_specs.key = 'props.date'
  );

-- Existing Patreon finance stat widgets predate display metadata. Preserve the
-- stored values while making the dashboard render their normalized USD unit.
UPDATE dashboard_widgets
SET config_json = json_set(config_json, '$.format', 'currency', '$.currency', 'USD')
WHERE json_valid(config_json)
  AND json_extract(config_json, '$.app') = 'patreon'
  AND (lower(title) LIKE '%revenue%' OR lower(title) LIKE '%mrr%');
