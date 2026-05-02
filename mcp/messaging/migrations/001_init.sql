-- Messaging v0.1.
--
-- One unified `messages` table for inbound + outbound across channels.
-- v0.1 only ever stores channel='email', but the schema is shaped so
-- 'sms' and friends drop in without migration.
--
-- Addresses are always canonical URIs ("mailto:foo@bar.com",
-- "tel:+15551234", "apteva://contact/42") so the channel can be
-- inferred from the scheme alone.

CREATE TABLE messages (
  id                       INTEGER PRIMARY KEY,
  project_id               TEXT NOT NULL,
  channel                  TEXT NOT NULL,            -- 'email' (v0.1)
  direction                TEXT NOT NULL,            -- 'in' | 'out'

  -- envelope
  from_addr                TEXT NOT NULL,            -- URI
  to_addrs                 TEXT NOT NULL DEFAULT '[]', -- JSON URI[]
  cc_addrs                 TEXT NOT NULL DEFAULT '[]', -- JSON URI[]
  bcc_addrs                TEXT NOT NULL DEFAULT '[]', -- JSON URI[] — outbound only
  subject                  TEXT,
  body_text                TEXT,
  body_html                TEXT,
  headers                  TEXT NOT NULL DEFAULT '{}',  -- JSON map
  attachment_storage_ids   TEXT NOT NULL DEFAULT '[]',  -- JSON int[]

  -- threading (RFC 5322; applies to both directions)
  message_id_header        TEXT,                     -- the Message-ID: header (raw, with angle brackets)
  in_reply_to              TEXT,                     -- the In-Reply-To: header
  references_json          TEXT NOT NULL DEFAULT '[]',  -- JSON Message-ID[]

  -- delivery
  status                   TEXT NOT NULL,            -- pending|sent|delivered|bounced|complained|failed|received
  status_reason            TEXT,                     -- terse error or reason for terminal state
  provider_message_id      TEXT,                     -- SES MessageId (out) or null (in)
  idempotency_key          TEXT,                     -- caller-supplied dedup key (out only)

  -- routing (inbound only)
  route_target_app         TEXT,
  route_target_route       TEXT,
  route_status             TEXT,                     -- ok | no_match | target_failed | pending
  route_error              TEXT,                     -- last dispatch error if route_status=target_failed
  route_attempts           INTEGER NOT NULL DEFAULT 0,
  matched_recipient        TEXT,                     -- which to_addrs entry triggered the route
  matched_pattern          TEXT,                     -- the inbound_routes.pattern that won
  to_subaddress            TEXT,                     -- the captured "+tag" portion, if any (e.g. "T-1234")

  -- template (outbound only)
  template_id              INTEGER,

  created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  sent_at                  TIMESTAMP,                -- out
  received_at              TIMESTAMP,                -- in
  last_event_at            TIMESTAMP                 -- out: latest delivery_events.occurred_at
);

CREATE INDEX ix_msg_proj_dir_time   ON messages(project_id, direction, created_at DESC);
CREATE INDEX ix_msg_proj_chan_stat  ON messages(project_id, channel, status);
CREATE INDEX ix_msg_thread          ON messages(project_id, in_reply_to);
CREATE INDEX ix_msg_msgid_header    ON messages(project_id, message_id_header);
CREATE INDEX ix_msg_provider_id     ON messages(provider_message_id);
CREATE UNIQUE INDEX ix_msg_idem     ON messages(project_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- One row per delivery event from the provider (Delivery, Bounce,
-- Complaint, Reject). Outbound only — inbound mail's "received" is
-- the final state, no event stream.
CREATE TABLE delivery_events (
  id           INTEGER PRIMARY KEY,
  message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,                        -- delivered | bounced | complained | rejected
  recipient    TEXT,                                 -- which recipient this event is about
  reason       TEXT,                                 -- short reason ("hard-bounce", "complaint:abuse", …)
  raw          TEXT NOT NULL DEFAULT '{}',           -- full provider payload as JSON
  occurred_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX ix_evt_msg ON delivery_events(message_id, occurred_at);

-- Saved templates with simple {{var}} placeholders. v0.1 keeps
-- templates per-channel so the rendering knows whether subject is
-- meaningful. A future "campaign" layer can compose multi-channel
-- variants on top of this.
CREATE TABLE templates (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  channel       TEXT NOT NULL DEFAULT 'email',
  name          TEXT NOT NULL,
  subject       TEXT,
  body_text     TEXT,
  body_html     TEXT,
  vars_schema   TEXT NOT NULL DEFAULT '{}',          -- JSON Schema-ish hint for callers
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  deleted_at    TIMESTAMP
);

CREATE UNIQUE INDEX ix_tpl_name ON templates(project_id, channel, name)
  WHERE deleted_at IS NULL;

-- Address-pattern → app+route mapping for inbound dispatch.
-- Pattern is a recipient URI with optional '*' wildcards in the
-- local-part. Longest-match-wins; tie-broken by priority DESC.
CREATE TABLE inbound_routes (
  id            INTEGER PRIMARY KEY,
  project_id    TEXT NOT NULL,
  pattern       TEXT NOT NULL,                       -- canonical URI form, e.g. "mailto:support+*@acme.com"
  target_app    TEXT NOT NULL,
  target_route  TEXT NOT NULL,                       -- "/inbound" — the receiving app's HTTP route
  priority      INTEGER NOT NULL DEFAULT 100,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX ix_inbound_route_unique
  ON inbound_routes(project_id, pattern, target_app, target_route);
CREATE INDEX ix_inbound_route_proj ON inbound_routes(project_id, priority DESC);

-- Per-channel suppression list. A bounced email doesn't suppress
-- the same person's SMS — the address namespace is per channel.
-- Updated automatically by the bounce/complaint webhook; manual
-- override via suppression_add / suppression_remove tools.
CREATE TABLE suppressions (
  project_id    TEXT NOT NULL,
  channel       TEXT NOT NULL,                       -- 'email' (v0.1)
  address       TEXT NOT NULL,                       -- canonical URI lowercase
  reason        TEXT NOT NULL,                       -- 'hard-bounce' | 'complaint' | 'manual' | 'reject'
  source        TEXT NOT NULL DEFAULT 'auto',        -- 'auto' | 'manual'
  first_seen    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  last_seen     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, channel, address)
);

CREATE INDEX ix_suppr_recent ON suppressions(project_id, channel, last_seen DESC);
