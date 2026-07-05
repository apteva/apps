-- tapo v0.3.8: app-level camera pause.
--
-- Paused cameras stay registered but Apteva stops fetching snapshots,
-- streaming frames, and watching ONVIF events for them.
ALTER TABLE cameras ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
