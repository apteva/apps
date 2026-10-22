-- Occupancy reads only active human calls, irrespective of historical owners.
-- Keep the partial predicate in sync with liveOccupancySQL/capacity lifecycle.
CREATE INDEX idx_calls_live_occupancy ON calls(project_id,direction,id,status)
 WHERE peer_kind='human'
 AND status NOT IN ('completed','failed','busy','no-answer','canceled');
