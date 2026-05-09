-- Campaigns v0.1 — bulk-send orchestrator.
--
-- A campaign targets a CRM segment (audience source) and sends via
-- messaging. Per-recipient state lives in campaign_recipients; the
-- jobs app drives a tick loop that walks pending → sending → sent.
--
-- Lifetime asymmetry note: campaign_recipients can grow large
-- (audience × campaign_count). v0.2 will add a retention config to
-- prune detail rows after N days while keeping aggregate stats.

CREATE TABLE campaigns (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  name                  TEXT    NOT NULL,
  description           TEXT,

  -- State machine. Transitions:
  --   draft ──schedule──► scheduled ──(jobs runs)──► materialising
  --                         │                            │
  --                       cancel                       (recipients inserted)
  --                                                      │
  --                                       sending ◄──────┘
  --                                       │  ▲  │
  --                                    pause│  │resume
  --                                       │  │  │
  --                                       paused │
  --                                          │  │
  --                                          ▼  ▼
  --                                          sent | cancelled | failed
  status                TEXT    NOT NULL DEFAULT 'draft',

  -- Channel + content. v0.1 keeps email + sms + whatsapp as the
  -- supported channels, mirroring messaging.
  channel               TEXT    NOT NULL,
  sender_address        TEXT,                           -- override; else falls through list / install defaults
  subject               TEXT,
  body_text             TEXT,
  body_html             TEXT,
  template_name         TEXT,                           -- when set, messaging renders the named template

  -- Audience source. Either segment_id (preferred) or list_id (used
  -- when the operator wants to send to the whole list). Materialise
  -- expands either into campaign_recipients rows.
  list_id               INTEGER,                        -- soft FK to crm.contact_lists.id
  segment_id            INTEGER,                        -- soft FK to crm.contact_segments.id

  -- Scheduling.
  schedule_kind         TEXT    NOT NULL DEFAULT 'immediate',  -- immediate | scheduled | recurring (v0.2)
  scheduled_at          TIMESTAMP,
  recurrence_cron       TEXT,                           -- v0.2

  -- Send-rate knobs. Per-campaign override of the install defaults.
  batch_size            INTEGER,
  tick_interval_seconds INTEGER,

  -- Jobs the campaign owns (so we can cancel them on pause/cancel).
  -- Comma-separated list of jobs.id values. Cheap; not normalised
  -- into a separate table because the count is bounded (1 materialise
  -- + 1 tick job per active campaign).
  job_ids               TEXT,

  -- Tracking toggles. Stored as flags so v0.2 can read them without
  -- migrating; the actual open/click endpoints come in v0.2.
  open_tracking         INTEGER NOT NULL DEFAULT 0,
  click_tracking        INTEGER NOT NULL DEFAULT 0,

  -- Lifecycle timestamps + author.
  created_by_user_id    INTEGER,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  started_at            TIMESTAMP,
  completed_at          TIMESTAMP,
  archived_at           TIMESTAMP,
  error                 TEXT
);
CREATE INDEX ix_camp_status ON campaigns(project_id, status, archived_at);
CREATE INDEX ix_camp_scheduled ON campaigns(project_id, scheduled_at) WHERE scheduled_at IS NOT NULL;

-- One row per (campaign, contact). Status walks through the per-row
-- state machine. Address is denormalised at materialise-time so a
-- subsequent contact-channel rotation doesn't change historical sends.
CREATE TABLE campaign_recipients (
  id              INTEGER PRIMARY KEY,
  campaign_id     INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  project_id      TEXT    NOT NULL,
  contact_id      INTEGER NOT NULL,
  address         TEXT    NOT NULL,                     -- denormalised at materialise
  status          TEXT    NOT NULL DEFAULT 'pending',
  -- Possible status values:
  --   pending      — waiting for tick to claim
  --   sending      — claimed by tick, send in flight
  --   sent         — messaging accepted; provider delivery pending
  --   delivered    — provider confirmed delivery (via reconcile)
  --   bounced      — hard bounce
  --   complained   — spam complaint
  --   failed       — send error (transient or permanent; attempt_count > max_retries)
  --   skipped      — pre-flight skip (suppressed, missing channel, …)
  --   unsubscribed — clicked unsubscribe token; future sends will skip
  messaging_id    INTEGER,                              -- the row id in messaging.messages
  attempt_count   INTEGER NOT NULL DEFAULT 0,
  last_attempt_at TIMESTAMP,
  sent_at         TIMESTAMP,
  delivered_at    TIMESTAMP,
  error           TEXT,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- One recipient row per (campaign, contact). Lets us idempotently
-- re-materialise without duplicate sends.
CREATE UNIQUE INDEX ux_camp_recipient ON campaign_recipients(campaign_id, contact_id);
-- Hot-path index for tick loop's "claim N pending" query.
CREATE INDEX ix_camp_recip_pending ON campaign_recipients(campaign_id, status);

-- HMAC-verified unsubscribe tokens. One per recipient; persisted so
-- the public /unsubscribe endpoint can validate without recomputing.
-- 32-byte random tokens, base64url-encoded → 43-char strings.
CREATE TABLE campaign_unsubscribe_tokens (
  token        TEXT    PRIMARY KEY,
  campaign_id  INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  recipient_id INTEGER NOT NULL REFERENCES campaign_recipients(id) ON DELETE CASCADE,
  project_id   TEXT    NOT NULL,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  used_at      TIMESTAMP
);
CREATE INDEX ix_unsub_recipient ON campaign_unsubscribe_tokens(recipient_id);

-- Single-row config table for the auto-generated unsubscribe secret.
-- Only used when the install's `unsubscribe_secret` config is blank.
CREATE TABLE campaign_runtime_config (
  k TEXT PRIMARY KEY,
  v TEXT
);
