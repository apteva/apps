UPDATE devices
SET protocol = 'apteva.devices/v1'
WHERE protocol <> 'apteva.devices/v1';

UPDATE devices
SET manifest_json = json_set(manifest_json, '$.protocol', 'apteva.devices/v1')
WHERE json_extract(manifest_json, '$.protocol') IS NOT NULL
  AND json_extract(manifest_json, '$.protocol') <> 'apteva.devices/v1';
