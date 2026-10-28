-- A lifecycle event the gateway permanently rejects (4xx other than 408/429)
-- is parked: published_at is set so it leaves the unpublished queue, and the
-- rejection is kept here. Parked rows: published_at <> '' AND publish_error <> ''.
ALTER TABLE call_events ADD COLUMN publish_error TEXT NOT NULL DEFAULT '';
