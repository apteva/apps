-- seo v0.6.1 -- selectable automatic rank refresh cadence.
--
-- Existing trackers retain the v0.6.0 daily policy. Weekly and monthly
-- trackers use their normal check depth on each run; the deeper Sunday scan
-- remains part of the daily policy only.

ALTER TABLE serp_trackers
    ADD COLUMN frequency TEXT NOT NULL DEFAULT 'daily'
        CHECK (frequency IN ('daily', 'weekly', 'monthly'));
