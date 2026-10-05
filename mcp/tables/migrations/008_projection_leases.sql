-- Add fencing tokens to projection queue claims. A worker that loses its
-- lease can no longer publish over a newer worker's result.
ALTER TABLE projection_queue ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
