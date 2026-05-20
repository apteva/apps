-- trips v0.6 — mirror the trip itself + its destinations into the
-- shared "Trips" calendar.
--
-- Until now only transport legs, accommodations and activities became
-- calendar events, so a freshly-planned trip (dates set, maybe a few
-- destinations, no bookings yet) produced an empty calendar. These two
-- columns let us attach an all-day block to the trip's overall span and
-- to each destination's arrive→depart window, the same way the child
-- item tables already carry calendar_event_id.

ALTER TABLE trips        ADD COLUMN calendar_event_id INTEGER;
ALTER TABLE destinations ADD COLUMN calendar_event_id INTEGER;
