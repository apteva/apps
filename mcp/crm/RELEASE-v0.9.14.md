# CRM v0.9.14

- Collapse layout-only blank lines when converting HTML-only inbound emails to readable text. Original plain-text email bodies remain unchanged.
- Guard older inbound email display against excessive blank-line gaps without modifying stored content.
- Add opt-in formatting recovery for a verified original only when the stored body exactly matches CRM's former HTML conversion. Edited or unrelated complete bodies are never overwritten.
- Add opt-in recovery of missing From metadata from the original Messaging email, matched by project, source install, message ID and RFC Message-ID. Existing sender and private audit fields remain untouched.
- Recovery defaults to a dry run, checks concurrent changes, and preserves contacts, conversations, timestamps, attachments, statuses, priorities and events. It never sends or redispatches mail.

Includes all v0.9.13 recipient ownership safeguards. No database migrations, Messaging changes or automatic historical data rewrites.
