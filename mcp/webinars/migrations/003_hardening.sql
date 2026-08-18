-- Webinars v0.2 — hardening.
--
-- Five themes, in dependency order:
--   1. normalize every timestamp column to RFC3339 UTC,
--   2. make reminders + phone-only registrations idempotent,
--   3. give each webinar an atomic sequence counter,
--   4. project/webinar-scope the engagement write tables,
--   5. add the indexes the workers scan on, and the ones retention
--      pruning needs.
--
-- Themes 1 and 2 are ordered on purpose: the reminder de-duplication in
-- (2) groups by scheduled_for, so it has to run after (1) has made those
-- values comparable.

-- ─── 1. Timestamp normalization ───────────────────────────────────
--
-- Pre-v0.2 writes went through SQL CURRENT_TIMESTAMP, which renders
-- "2026-08-18 09:06:00". time.Parse(time.RFC3339, …) rejects that, so
-- the offer broadcaster's `time.Parse(RFC3339, started_at)` errored on
-- EVERY live webinar and `continue`d — no scripted offer ever fired.
-- The same layout also breaks the lexical timestamp comparisons the
-- slot sweep and retention pruning rely on.
--
-- Rewrite the legacy layout in place. Rows that already contain a 'T'
-- are RFC3339 and are left alone.

UPDATE webinars SET scheduled_at      = replace(scheduled_at,' ','T')      || 'Z' WHERE scheduled_at      IS NOT NULL AND scheduled_at      <> '' AND instr(scheduled_at,'T')      = 0;
UPDATE webinars SET created_at        = replace(created_at,' ','T')        || 'Z' WHERE created_at        IS NOT NULL AND created_at        <> '' AND instr(created_at,'T')        = 0;
UPDATE webinars SET started_at        = replace(started_at,' ','T')        || 'Z' WHERE started_at        IS NOT NULL AND started_at        <> '' AND instr(started_at,'T')        = 0;
UPDATE webinars SET ended_at          = replace(ended_at,' ','T')          || 'Z' WHERE ended_at          IS NOT NULL AND ended_at          <> '' AND instr(ended_at,'T')          = 0;
UPDATE webinars SET replay_expires_at = replace(replay_expires_at,' ','T') || 'Z' WHERE replay_expires_at IS NOT NULL AND replay_expires_at <> '' AND instr(replay_expires_at,'T') = 0;

UPDATE webinar_registrants SET registered_at = replace(registered_at,' ','T') || 'Z' WHERE registered_at IS NOT NULL AND registered_at <> '' AND instr(registered_at,'T') = 0;

UPDATE webinar_slots SET starts_at  = replace(starts_at,' ','T')  || 'Z' WHERE starts_at  IS NOT NULL AND starts_at  <> '' AND instr(starts_at,'T')  = 0;
UPDATE webinar_slots SET ends_at    = replace(ends_at,' ','T')    || 'Z' WHERE ends_at    IS NOT NULL AND ends_at    <> '' AND instr(ends_at,'T')    = 0;
UPDATE webinar_slots SET created_at = replace(created_at,' ','T') || 'Z' WHERE created_at IS NOT NULL AND created_at <> '' AND instr(created_at,'T') = 0;
UPDATE webinar_slots SET updated_at = replace(updated_at,' ','T') || 'Z' WHERE updated_at IS NOT NULL AND updated_at <> '' AND instr(updated_at,'T') = 0;

UPDATE webinar_attendance SET joined_at      = replace(joined_at,' ','T')      || 'Z' WHERE joined_at      IS NOT NULL AND joined_at      <> '' AND instr(joined_at,'T')      = 0;
UPDATE webinar_attendance SET last_heartbeat = replace(last_heartbeat,' ','T') || 'Z' WHERE last_heartbeat IS NOT NULL AND last_heartbeat <> '' AND instr(last_heartbeat,'T') = 0;
UPDATE webinar_attendance SET left_at        = replace(left_at,' ','T')        || 'Z' WHERE left_at        IS NOT NULL AND left_at        <> '' AND instr(left_at,'T')        = 0;

UPDATE webinar_offers SET shown_at = replace(shown_at,' ','T') || 'Z' WHERE shown_at IS NOT NULL AND shown_at <> '' AND instr(shown_at,'T') = 0;

UPDATE webinar_offer_clicks SET clicked_at = replace(clicked_at,' ','T') || 'Z' WHERE clicked_at IS NOT NULL AND clicked_at <> '' AND instr(clicked_at,'T') = 0;

UPDATE webinar_polls SET opened_at = replace(opened_at,' ','T') || 'Z' WHERE opened_at IS NOT NULL AND opened_at <> '' AND instr(opened_at,'T') = 0;
UPDATE webinar_polls SET closes_at = replace(closes_at,' ','T') || 'Z' WHERE closes_at IS NOT NULL AND closes_at <> '' AND instr(closes_at,'T') = 0;

UPDATE webinar_poll_responses SET answered_at = replace(answered_at,' ','T') || 'Z' WHERE answered_at IS NOT NULL AND answered_at <> '' AND instr(answered_at,'T') = 0;

UPDATE webinar_chat SET created_at = replace(created_at,' ','T') || 'Z' WHERE created_at IS NOT NULL AND created_at <> '' AND instr(created_at,'T') = 0;

UPDATE webinar_reminders SET scheduled_for = replace(scheduled_for,' ','T') || 'Z' WHERE scheduled_for IS NOT NULL AND scheduled_for <> '' AND instr(scheduled_for,'T') = 0;
UPDATE webinar_reminders SET sent_at       = replace(sent_at,' ','T')       || 'Z' WHERE sent_at       IS NOT NULL AND sent_at       <> '' AND instr(sent_at,'T')       = 0;

-- ─── 2a. Idempotent reminder scheduling ───────────────────────────
--
-- webinar_reminders had no uniqueness at all, so re-running
-- scheduleRemindersForRegistrant (which registration did on every
-- duplicate submit) inserted a second full set of pending rows. Only
-- messaging-side de-duplication hid the double send.
--
-- (registrant, channel, lead_label, scheduled_for) is the natural key:
-- it makes re-scheduling a no-op while still letting a genuine reschedule
-- to a NEW time create a new row.
DELETE FROM webinar_reminders
 WHERE id NOT IN (
   SELECT MIN(id) FROM webinar_reminders
    GROUP BY registrant_id, channel, lead_label, scheduled_for
 );

CREATE UNIQUE INDEX ux_reminder_slot
  ON webinar_reminders(registrant_id, channel, lead_label, scheduled_for);

-- regenerateReminders deletes by (webinar_id, status); the existing
-- partial index is keyed (status, scheduled_for) and can't serve it, so
-- the DELETE scanned every pending row across every webinar.
CREATE INDEX ix_reminder_webinar_status ON webinar_reminders(webinar_id, status);

-- Retention pruning walks non-pending rows by time.
CREATE INDEX ix_reminder_status_time ON webinar_reminders(status, scheduled_for);

-- ─── 2b. Idempotent phone-only registration ───────────────────────
--
-- ux_reg_email only covers rows WITH an email, so a phone-only double
-- submit created a second registrant, a second join token and a second
-- full set of SMS reminders. The partial index below closes that
-- without constraining the (much more common) email+phone rows, where
-- email remains the identity.
--
-- Destructive step, deliberately: pre-existing phone-only duplicates
-- are collapsed onto the EARLIEST row, because that's the one whose
-- join_token already went out over SMS. Cascades take the losing rows'
-- attendance/reminders with them.
DELETE FROM webinar_registrants
 WHERE phone IS NOT NULL AND phone <> '' AND (email IS NULL OR email = '')
   AND id NOT IN (
     SELECT MIN(id) FROM webinar_registrants
      WHERE phone IS NOT NULL AND phone <> '' AND (email IS NULL OR email = '')
      GROUP BY webinar_id, phone
   );

CREATE UNIQUE INDEX ux_reg_phone ON webinar_registrants(webinar_id, phone)
  WHERE phone IS NOT NULL AND phone <> '' AND (email IS NULL OR email = '');

-- ─── 3. Atomic per-webinar sequences ──────────────────────────────
--
-- The live-room cursor (`sequence > since`) requires strictly
-- increasing, never-reused sequence numbers. MAX(sequence)+1 computed
-- in a separate statement gave two concurrent chat posts the same
-- number, and the reader's cursor then skipped past one of them
-- permanently. One counter row per (webinar, kind), bumped with a
-- single atomic upsert.
CREATE TABLE webinar_sequences (
  webinar_id INTEGER NOT NULL REFERENCES webinars(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL DEFAULT 'event',  -- event | manual_send
  value      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (webinar_id, kind)
);

-- Seed the live-room counter past whatever the old scan already handed
-- out, so cursors held by in-flight viewers stay valid.
INSERT INTO webinar_sequences (webinar_id, kind, value)
SELECT w.id, 'event',
       MAX(
         COALESCE((SELECT MAX(sequence) FROM webinar_chat   c WHERE c.webinar_id = w.id), 0),
         COALESCE((SELECT MAX(sequence) FROM webinar_offers o WHERE o.webinar_id = w.id), 0),
         COALESCE((SELECT MAX(sequence) FROM webinar_polls  p WHERE p.webinar_id = w.id), 0)
       )
  FROM webinars w;

-- webinar_polls had no (webinar_id, sequence) index at all, so both the
-- old sequence scan and the /events poll arm were table scans.
CREATE INDEX ix_poll_timeline ON webinar_polls(webinar_id, sequence);

-- ─── 4. Scope the engagement write tables ─────────────────────────
--
-- poll_id / offer_id arrive from the browser as sequential integer PKs.
-- webinar_poll_responses carried no project_id or webinar_id at all, so
-- there was nothing to verify a submitted id against — any registrant
-- of any webinar could stuff another webinar's (or, under scope=global,
-- another tenant's) poll results and offer CTR.
ALTER TABLE webinar_poll_responses ADD COLUMN project_id TEXT;
ALTER TABLE webinar_poll_responses ADD COLUMN webinar_id INTEGER;
UPDATE webinar_poll_responses
   SET project_id = (SELECT p.project_id FROM webinar_polls p WHERE p.id = poll_id),
       webinar_id = (SELECT p.webinar_id FROM webinar_polls p WHERE p.id = poll_id);
CREATE INDEX ix_poll_resp_scope ON webinar_poll_responses(webinar_id, poll_id);

ALTER TABLE webinar_offer_clicks ADD COLUMN webinar_id INTEGER;
UPDATE webinar_offer_clicks
   SET webinar_id = (SELECT o.webinar_id FROM webinar_offers o WHERE o.id = offer_id);
CREATE INDEX ix_offer_click_scope ON webinar_offer_clicks(webinar_id, clicked_at DESC);
CREATE INDEX ix_offer_click_time  ON webinar_offer_clicks(clicked_at);

-- ─── 5. Worker + pruning indexes ──────────────────────────────────

-- attendance-decay marks stale rows by last_heartbeat alone; the
-- existing partial index leads with webinar_id and can't seek on it.
CREATE INDEX ix_attendance_stale ON webinar_attendance(last_heartbeat)
  WHERE left_at IS NULL;

-- The attended_* promotion scans by (source, last_heartbeat) now that
-- it's scoped to the current sweep window instead of all time.
CREATE INDEX ix_attendance_promote ON webinar_attendance(source, last_heartbeat);

-- ...and lands on webinar_registrants, which had no index covering the
-- attendance flags.
CREATE INDEX ix_reg_attended ON webinar_registrants(webinar_id, attended_live);
CREATE INDEX ix_reg_attended_replay ON webinar_registrants(webinar_id, attended_replay);

-- Slot status transitions sweep by (status, starts_at) across all
-- webinars; the 002 indexes both lead with project_id.
CREATE INDEX ix_webinar_slots_status_time ON webinar_slots(status, starts_at);

-- Chat retention pruning walks by age.
CREATE INDEX ix_chat_created ON webinar_chat(created_at);
