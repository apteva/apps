-- v0.13.30: suppressions cover both outbound recipients and inbound
-- senders, and can target either an exact address or an email domain.
--
-- Existing rows are exact address suppressions. Domain suppressions use
-- the same address column to avoid a disruptive table rebuild.

ALTER TABLE suppressions
  ADD COLUMN kind TEXT NOT NULL DEFAULT 'address';

CREATE INDEX ix_suppr_kind
  ON suppressions(project_id, channel, kind, address);
