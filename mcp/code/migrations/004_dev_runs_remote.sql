-- Apteva Code v0.6.0 — remote dev runs.
--
-- Mobile repos (iOS / Android) don't run as a local child process; the
-- Code sidecar delegates to the Simulator app, which boots a sim,
-- builds the source, installs, launches, and streams the screen back.
-- These columns capture that delegation so repos_dev_status /
-- repos_dev_stop can route correctly.
--
--   runner     '' = local child process (web frameworks); 'simulator' =
--              delegated to the Simulator app.
--   sim_id     the Simulator app's sim handle (AVD name / iOS UDID).
--   stream_url the live-stream WebSocket URL the panel embeds.
--
-- Local dev runs leave all three empty, so existing behavior is
-- unchanged.

ALTER TABLE dev_runs ADD COLUMN runner     TEXT NOT NULL DEFAULT '';
ALTER TABLE dev_runs ADD COLUMN sim_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE dev_runs ADD COLUMN stream_url TEXT NOT NULL DEFAULT '';
