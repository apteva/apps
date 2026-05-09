-- Lists — explicit, configurable buckets of contacts.
--
-- A contact can belong to N lists (Brevo / Klaviyo / ActiveCampaign
-- model, not Mailchimp's single-audience-per-contact). Each list
-- carries its own sender defaults so the SaaS-1 / SaaS-2 case can
-- live in one CRM install with one contact record but two send
-- identities.
--
-- Lists also drive the messaging coupling: Settings' wire-up button
-- writes one inbound_route per list using `inbound_route_pattern`
-- (e.g. "*@saas1.example.com"), and the /inbound webhook resolves
-- list_id from the matched_pattern to auto-add the contact to that
-- list.

CREATE TABLE contact_lists (
  id                    INTEGER PRIMARY KEY,
  project_id            TEXT    NOT NULL,
  slug                  TEXT    NOT NULL,                -- short, kebab-case identifier
  name                  TEXT    NOT NULL,                -- display
  description           TEXT,

  -- Sender defaults — used by contacts_send_message when called with
  -- list_id. Falls through to install-level defaults when blank.
  default_sender_email  TEXT,
  default_sender_phone  TEXT,

  -- Inbound routing pattern — used by Settings wire-up and the
  -- /inbound webhook for list resolution. Format mirrors messaging's
  -- inbound_routes.pattern (exact, "*@domain", "support+*@domain").
  inbound_route_pattern TEXT,

  archived_at           TIMESTAMP,
  created_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- One slug per project. Lookup-friendly.
CREATE UNIQUE INDEX ux_list_slug ON contact_lists(project_id, slug);
-- Pattern lookup for inbound resolution.
CREATE INDEX ix_list_pattern ON contact_lists(project_id, inbound_route_pattern) WHERE inbound_route_pattern IS NOT NULL;
-- Active-list listing.
CREATE INDEX ix_list_active ON contact_lists(project_id, archived_at);

-- Membership join table. Cheap inserts/deletes; one row per
-- (list, contact). Cascades when the contact is hard-deleted.
CREATE TABLE contact_list_members (
  list_id     INTEGER NOT NULL REFERENCES contact_lists(id) ON DELETE CASCADE,
  contact_id  INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  project_id  TEXT    NOT NULL,
  added_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  source      TEXT,                                       -- "human" | "agent:<id>" | "messaging:inbound" | "import"
  PRIMARY KEY (list_id, contact_id)
);
-- Reverse lookup: which lists is a contact on?
CREATE INDEX ix_list_member_contact ON contact_list_members(project_id, contact_id);
