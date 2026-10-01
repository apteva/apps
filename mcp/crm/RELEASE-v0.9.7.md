# CRM v0.9.7

Reduces the recurring cost of Messaging suppression reconciliation for large
CRM projects.

- The five-minute worker now reads only pending soft-bounce retries, using a
  partial index over those rows. It no longer fetches every suppression or
  walks every delivery route.
- A full Messaging-to-CRM safety sweep runs every 30 minutes. It is always
  performed so a missed external event can still be repaired.
- Full sweeps stage the suppression snapshot and apply exact-address and
  domain matches with two set-based SQL updates. Unchanged routes are not
  rewritten, and a newer event or send-time check wins over an older snapshot.
- Existing event-driven updates, new-route checks, and send-time Messaging
  suppression checks continue to run immediately.

Regression coverage includes a 12,001-route project, unchanged sweeps,
exact-address precedence, newer-event protection, and the retry query plan.
The only schema change is a new partial index; no CRM records are deleted or
rewritten during migration.
