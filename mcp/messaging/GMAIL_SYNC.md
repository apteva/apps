# Gmail mailbox synchronization

Messaging uses the app SDK's `gmail-mailbox-sync` worker every two minutes.
There is no independent cron process or Gmail polling goroutine. Registered
Gmail senders and their bound connections determine which mailboxes can sync;
the SDK's dispatched project and authenticated mailbox ownership are enforced.

## Catch-up and ongoing sync

On first connection, and once when upgrading an existing connection to
Messaging 0.13.57, the worker imports the previous **seven days** of inbox,
archived and sent mail. This is a bounded recent-history import, not an import
of the entire mailbox. Spam, trash and drafts are excluded.

Catch-up processes at most one 100-message list page per tick. Its fixed time
window and continuation token are stored in `gmail_sync_state`, so subsequent
ticks and app restarts resume instead of re-importing the first page. New-mail
history is polled during catch-up, even if a historical page fails. Expired
Gmail history cursors restart this resumable seven-day catch-up.

Both new messages and transitions into SENT/INBOX are processed. Provider
message IDs deduplicate both directions, including messages already sent
through Apteva. Gmail's `internalDate` is retained as the original message
timestamp, with the MIME Date header as fallback.

Messages and attachment/routing jobs commit together. The SDK's
`messaging-recovery` worker retries downstream failures without stopping
mailbox synchronization. Raw attachment bytes are removed from completed jobs.

## CRM boundary

Incoming messages continue through the existing ownership, suppression and
routing checks. Eligible mail is forwarded to the configured inbox consumer,
including CRM, with stable delivery IDs.

Emails sent directly from Gmail are imported as `direction=out`, `status=sent`
records in Messaging. Import never calls a send transport, never routes these
records to an inbound-only consumer, and never emits a historical
`message.sent`/`message.received` business event. A metadata-only `message.event`
refreshes the Messaging UI after a sent import completes.

CRM is unchanged by this release. Showing Gmail-sent history in CRM requires a
separate sent-message import contract in CRM; it must not be emulated by
delivering outgoing mail to `messaging_inbound_receive`.
