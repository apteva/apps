-- Provider response envelopes are not used by the app and older versions could
-- persist authentication fields returned by an upstream API.
UPDATE networks
SET metadata_json = '{}', updated_at = CURRENT_TIMESTAMP
WHERE metadata_json <> '{}';
