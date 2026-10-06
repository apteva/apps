# Messaging v0.13.57

Messaging-only Gmail synchronization fix using app SDK v0.92.0 workers.

- The SDK `gmail-mailbox-sync` worker polls every two minutes and is now also
  declared in the manifest. SDK project dispatch is respected.
- New and existing Gmail connections automatically catch up the previous seven
  days of incoming, archived and sent mail, one 100-message page per tick.
  Fixed-window pagination survives restarts. New-mail history continues during
  catch-up; expired history cursors schedule a resumable catch-up.
- Emails sent directly from Gmail are stored as outbound, already-sent records.
  Draft-to-SENT transitions are synchronized; drafts, spam and trash are skipped.
- Provider IDs deduplicate both directions and Apteva-sent messages. Pending
  Apteva sends observed in Gmail are reconciled by RFC Message-ID without
  duplicating or resending them. Original Gmail timestamps, threading metadata,
  recipients and attachments are retained.
- Committed attachment/routing jobs are retried by the SDK recovery worker;
  downstream outages do not stall the entire mailbox. Authenticated mailbox
  ownership and the v0.13.56 inbound isolation protections remain enforced.
- Sent imports cannot enter an inbound-only CRM route or replay historical
  sent/received business events. A metadata-only event refreshes Messaging.

Migration 016 is additive and preserves existing message IDs, content, sync
cursors and sender configuration. All existing Messaging UI, widgets and
providers are preserved. CRM code is unchanged: incoming mail uses the existing
CRM route; Gmail-sent history is visible in Messaging only until CRM gains a
separate sent-message import contract. This release does not deploy or upgrade
any client instance.

Validation: full Go tests, full race tests, go vet and source build with the
released SDK dependency (GOWORK=off), plus migration-preservation, SDK-worker,
pagination/restart, ownership, attachment-recovery, deduplication and no-resend
regressions.
