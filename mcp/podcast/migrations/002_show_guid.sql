-- Add a stable show-level Podcasting 2.0 GUID for podcast:guid.

ALTER TABLE shows ADD COLUMN podcast_guid TEXT NOT NULL DEFAULT '';

UPDATE shows
SET podcast_guid =
  lower(hex(randomblob(4))) || '-' ||
  lower(hex(randomblob(2))) || '-4' ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  substr('89ab', abs(random()) % 4 + 1, 1) ||
  substr(lower(hex(randomblob(2))), 2) || '-' ||
  lower(hex(randomblob(6)))
WHERE podcast_guid = '';
