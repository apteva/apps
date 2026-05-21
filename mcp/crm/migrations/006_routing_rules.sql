-- Inbound routing rules.
--
-- Generalizes the brittle "list.inbound_route_pattern must equal
-- messaging's matched_pattern" coupling into a proper rule engine. Each
-- rule matches inbound on the recipient address (which of OUR addresses
-- it was sent to) and/or the sender address (who/what wrote), and
-- applies actions: add the contact to a list and/or tag it.
--
-- Both match fields are patterns (NULL/empty = "any"):
--   exact         alice@acme.com
--   domain        @acme.com   or   *@acme.com
--   local-any     support@*
--   any           *           (or NULL)
--
-- A rule with both recipient + sender set requires BOTH to match (AND).
-- All enabled rules are evaluated in priority order; every match's
-- actions apply (additive — a contact can join several lists / tags).
-- Owner assignment is intentionally NOT here: there's no users/teams
-- model behind contacts.owner_user_id yet.

CREATE TABLE routing_rules (
  id              INTEGER PRIMARY KEY,
  project_id      TEXT    NOT NULL,
  name            TEXT,

  match_recipient TEXT,                 -- pattern vs the addressed-to; NULL = any
  match_sender    TEXT,                 -- pattern vs the from address; NULL = any

  add_list_id     INTEGER REFERENCES contact_lists(id) ON DELETE SET NULL,
  add_tag         TEXT,

  priority        INTEGER NOT NULL DEFAULT 0,   -- lower first
  enabled         INTEGER NOT NULL DEFAULT 1,

  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  archived_at     TIMESTAMP
);

CREATE INDEX ix_routing_rules ON routing_rules(project_id, priority)
  WHERE archived_at IS NULL AND enabled = 1;
