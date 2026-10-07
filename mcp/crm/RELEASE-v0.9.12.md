# CRM v0.9.12

Makes message destinations visible in the CRM inbox and conversation detail.

- Inbox rows and the Customer Inbox widget show the latest message's recorded
  recipient, including a tooltip for long addresses.
- Each message displays From, To, optional CC/BCC, and Received at when the
  receiving address is recorded. Multiple recipients wrap and remain copyable.
- Addresses come from that message's stored envelope, never the contact's
  primary address or accumulated thread participants. Missing historical
  metadata is shown as Unknown.

The API exposes an additive, allow-listed message-address projection. Private
provider metadata stays private. Historical records work without a backfill or
additional Messaging requests, and malformed old metadata cannot break the inbox.

No database migrations, record edits, routing changes, or sending changes.
All prior CRM fixes remain included, including fast cancellable audience counts
and read-only inbox discovery.
