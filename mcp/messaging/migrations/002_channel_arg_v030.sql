-- v0.3.0: address shape change.
--
-- v0.1/v0.2 stored addresses as canonical URIs ("mailto:foo@bar.com")
-- so the URI scheme could carry the channel. v0.3 takes channel as
-- an explicit send_message argument and stores plain addresses; URI
-- prefixes go away. This migration normalises any rows written under
-- the old contract so v0.3 reads work without conditional URI handling.
--
-- All UPDATEs are idempotent and safe to re-run on a fresh DB
-- (no-op when no row matches).

-- messages: from_addr, to_addrs, cc_addrs, bcc_addrs.
UPDATE messages
   SET from_addr = SUBSTR(from_addr, 8)
 WHERE from_addr LIKE 'mailto:%';

-- to_addrs / cc_addrs / bcc_addrs are JSON arrays of URIs. SQLite
-- doesn't ship JSON-aware UPDATEs in the default migration runner,
-- so we use REPLACE on the literal "mailto:" substring. Safe because
-- "mailto:" doesn't appear in legitimate email content.
UPDATE messages
   SET to_addrs = REPLACE(to_addrs, 'mailto:', '')
 WHERE to_addrs LIKE '%mailto:%';
UPDATE messages
   SET cc_addrs = REPLACE(cc_addrs, 'mailto:', '')
 WHERE cc_addrs LIKE '%mailto:%';
UPDATE messages
   SET bcc_addrs = REPLACE(bcc_addrs, 'mailto:', '')
 WHERE bcc_addrs LIKE '%mailto:%';

-- matched_recipient on inbound rows.
UPDATE messages
   SET matched_recipient = SUBSTR(matched_recipient, 8)
 WHERE matched_recipient LIKE 'mailto:%';

-- suppressions: keyed on (project_id, channel, address).
-- Only mailto-style values existed under v0.2 (channel was always
-- 'email'); strip them to plain addresses.
UPDATE suppressions
   SET address = SUBSTR(address, 8)
 WHERE address LIKE 'mailto:%';

-- inbound_routes: gain an explicit channel column. Until now the
-- channel was implied by the URI scheme of the pattern; v0.3 makes
-- it a first-class field, defaulting to 'email' for legacy rows.
ALTER TABLE inbound_routes
  ADD COLUMN channel TEXT NOT NULL DEFAULT 'email';

-- Strip mailto: from existing patterns and tag them as email.
UPDATE inbound_routes
   SET pattern = SUBSTR(pattern, 8),
       channel = 'email'
 WHERE pattern LIKE 'mailto:%';

-- Patterns that didn't have a scheme prefix already get 'email' via
-- the column default; nothing to migrate beyond the LIKE rows above.

-- Rebuild the unique index to include channel — the same address
-- can be a route for email AND for sms when SMS lands.
DROP INDEX IF EXISTS ix_inbound_route_unique;
CREATE UNIQUE INDEX ix_inbound_route_unique
  ON inbound_routes(project_id, channel, pattern, target_app, target_route);
