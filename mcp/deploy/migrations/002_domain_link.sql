-- Link the deploy app's `domain` field to the domains app.
--
-- domain_record_id   — the DNS record's provider-side ID (or the
--                      domain_records_set "action" payload echoed
--                      back as a stable handle), so detach can
--                      target the same record we wrote.
-- domain_attached_at — set when the DNS record was confirmed
--                      written; cleared on detach. Lets the panel
--                      distinguish "user typed a domain" from
--                      "DNS is actually pointing here".

ALTER TABLE deployments ADD COLUMN domain_record_id   TEXT      NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN domain_attached_at TIMESTAMP;
