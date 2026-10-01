# CRM v0.9.8

Recovers inbound email content that was previously stored as only its subject.

- Whitespace-only plain-text parts now fall back to readable text extracted
  from the HTML body. Script, stylesheet, and document metadata remain excluded.
- A duplicate delivery may fill an empty or subject-only email activity body.
  Complete bodies, activity/conversation IDs, status, priority, timestamps,
  attachments, and existing workflow events are preserved.
- `contacts_refresh_message_body` recovers one existing inbound email activity
  directly from its original in the bound Messaging installation. It defaults
  to a dry run and checks the contact, project, source install, and RFC Message-ID
  before writing. Legacy source IDs are allowed only after matching that header.
  Recovery never sends mail or redispatches a message.
- Both source builds and tests use app-sdk v0.90.0.

No schema migration or bulk rewrite is introduced. Regression coverage exercises
HTML-only ingestion, missing-body retries, legacy recovery, dry runs, mismatched
provenance, complete-body preservation, and concurrent edits.
